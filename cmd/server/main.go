package main

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/channels"
	"flowops-ai/internal/database"
	"flowops-ai/internal/delivery"
	"flowops-ai/internal/handlers"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
)

func main() {
	loadDotEnv(".env")
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://postgres:secret@127.0.0.1:5433/flowops?sslmode=disable"
	}

	pool, err := database.NewPool(context.Background(), databaseURL)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := database.Migrate(context.Background(), pool); err != nil {
		logger.Error("failed to initialize database", "error", err)
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	knowledgeBase, err := knowledge.Load("knowledge_base")
	if err != nil {
		logger.Error("failed to load knowledge base", "error", err)
		os.Exit(1)
	}
	knowledgeHandler := handlers.NewKnowledgeHandler(knowledgeBase, pool)
	if err := knowledgeBase.PurgeExpired(); err != nil {
		logger.Error("failed to purge knowledge at startup", "error", err)
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := knowledgeBase.PurgeExpired(); err != nil {
				logger.Error("knowledge TTL purge failed", "error", err)
			}
		}
	}()
	channelConfig := channels.Config{
		TelegramToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		MaxToken:       os.Getenv("MAX_BOT_TOKEN"),
		MaxAPIURL:      os.Getenv("MAX_API_URL"),
		MaxInsecureTLS: envBool("MAX_INSECURE_TLS", false),
		MailIMAPHost:   os.Getenv("MAIL_IMAP_HOST"), MailSMTPHost: os.Getenv("MAIL_SMTP_HOST"),
		MailUser: os.Getenv("MAIL_USER"), MailPassword: os.Getenv("MAIL_PASSWORD"),
	}
	dispatcher := channels.NewDispatcher(channelConfig)
	callbacks, err := security.NewCallbacks(os.Getenv("FLOWOPS_CALLBACK_URLS"))
	if err != nil {
		logger.Error("invalid callback allowlist")
		os.Exit(1)
	}
	ticketAnalyzer := analyzer.New(knowledgeBase)
	mode, configuredProvider, _ := ticketAnalyzer.ActiveProvider()
	primaryProvider := "Local Heuristic only"
	if mode == "llm" {
		primaryProvider = strings.TrimSuffix(configuredProvider, " (РФ контур)")
	}
	externalLLMAllowed := mode == "llm"
	logger.Info("[AI] Configuration",
		"primary_provider", primaryProvider,
		"anonymized_data_to_external_llm", externalLLMAllowed,
		"sensitive_data_to_external_llm", envBool("FLOWOPS_AI_ALLOW_SENSITIVE", false),
	)
	ticketsHandler := handlers.NewTicketsHandler(pool, logger, ticketAnalyzer, dispatcher).WithCallbacks(callbacks)
	channelContext, stopChannels := context.WithCancel(context.Background())
	defer stopChannels()
	go delivery.New(pool, ticketsHandler.Deliver).Run(channelContext, logger)
	channels.Start(channelContext, channelConfig, ticketsHandler.IngestMessage, logger)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tickets", ticketsHandler.List)
	mux.HandleFunc("GET /api/tickets/{id}", ticketsHandler.Detail)
	mux.HandleFunc("POST /api/tickets", ticketsHandler.Create)
	mux.Handle("POST /api/v1/ingest", security.ProtectIngest(http.HandlerFunc(ticketsHandler.Ingest), os.Getenv("FLOWOPS_INGEST_TOKEN")))
	mux.HandleFunc("POST /api/tickets/{id}/feedback", ticketsHandler.Feedback)
	mux.HandleFunc("POST /api/tickets/{id}/reply", ticketsHandler.Reply)
	mux.HandleFunc("PATCH /api/tickets/{id}", ticketsHandler.Update)
	mux.HandleFunc("DELETE /api/tickets/reset", ticketsHandler.Reset)
	mux.HandleFunc("GET /api/stats", ticketsHandler.Stats)
	mux.HandleFunc("GET /api/audit", ticketsHandler.Audit)
	mux.HandleFunc("GET /api/knowledge", knowledgeHandler.List)
	mux.HandleFunc("POST /api/knowledge", knowledgeHandler.Create)
	mux.HandleFunc("PATCH /api/knowledge/{filename}", knowledgeHandler.Update)
	mux.HandleFunc("DELETE /api/knowledge/{filename}", knowledgeHandler.Delete)
	mux.Handle("/", noStoreFileServer(http.Dir("web")))

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           loggingMiddleware(logger, security.ProtectOperatorWrites(mux, os.Getenv("FLOWOPS_OPERATOR_TOKEN"))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server started", "addr", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	shutdownSignal := make(chan os.Signal, 1)
	signal.Notify(shutdownSignal, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	case sig := <-shutdownSignal:
		logger.Info("shutting down server", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" || os.Getenv(strings.TrimSpace(key)) != "" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		_ = os.Setenv(strings.TrimSpace(key), value)
	}
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		writer := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(writer, r)
		if !(r.Method == http.MethodGet && writer.status >= 200 && writer.status < 300 && isNoisyPollingPath(r.URL.Path)) {
			logger.Info("http request", "method", r.Method, "path", r.URL.Path, "status", writer.status, "duration_ms", time.Since(started).Microseconds()/1000, "response_bytes", writer.bytes)
		}
	})
}

func isNoisyPollingPath(path string) bool {
	switch path {
	case "/api/tickets", "/api/stats", "/api/audit":
		return true
	default:
		return false
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func noStoreFileServer(root http.Dir) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		http.FileServer(root).ServeHTTP(w, r)
	})
}
