package main

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/database"
	"flowops-ai/internal/handlers"
	"flowops-ai/internal/knowledge"
)

func main() {
	loadDotEnv(".env")
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

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
	ticketsHandler := handlers.NewTicketsHandler(pool, logger, analyzer.New(knowledgeBase))
	knowledgeHandler := handlers.NewKnowledgeHandler(knowledgeBase, pool)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tickets", ticketsHandler.List)
	mux.HandleFunc("POST /api/tickets", ticketsHandler.Create)
	mux.HandleFunc("POST /api/v1/ingest", ticketsHandler.Ingest)
	mux.HandleFunc("POST /api/tickets/{id}/feedback", ticketsHandler.Feedback)
	mux.HandleFunc("PATCH /api/tickets/{id}", ticketsHandler.Update)
	mux.HandleFunc("DELETE /api/tickets/reset", ticketsHandler.Reset)
	mux.HandleFunc("GET /api/stats", ticketsHandler.Stats)
	mux.HandleFunc("GET /api/knowledge", knowledgeHandler.List)
	mux.HandleFunc("POST /api/knowledge", knowledgeHandler.Create)
	mux.HandleFunc("PATCH /api/knowledge/{filename}", knowledgeHandler.Update)
	mux.HandleFunc("DELETE /api/knowledge/{filename}", knowledgeHandler.Delete)
	mux.Handle("/", noStoreFileServer(http.Dir("web")))

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           loggingMiddleware(logger, mux),
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
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "status", writer.status, "duration_ms", time.Since(started).Microseconds()/1000, "response_bytes", writer.bytes)
	})
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
