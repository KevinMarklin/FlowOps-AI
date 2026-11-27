package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
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
	"flowops-ai/internal/settings"
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
	if err := database.SyncDefaultWorkspace(context.Background(), pool, os.Getenv("FLOWOPS_OPERATOR_TOKEN")); err != nil {
		logger.Error("failed to synchronize default workspace", "error", err)
		os.Exit(1)
	}
	integrationsHandler := handlers.NewIntegrationsHandler(pool)
	loadBuilderConfig := func(ctx context.Context) (settings.BuilderConfig, error) {
		return integrationsHandler.LoadBuilderConfig(ctx)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if err := knowledge.MigrateRootMarkdown("knowledge_base"); err != nil {
		logger.Error("failed to migrate default knowledge base", "error", err)
		os.Exit(1)
	}
	knowledgeBase, err := knowledge.Load("knowledge_base/default")
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
	envChannelConfig := channels.Config{
		TelegramToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		MaxToken:       os.Getenv("MAX_BOT_TOKEN"),
		MaxAPIURL:      os.Getenv("MAX_API_URL"),
		MaxInsecureTLS: envBool("MAX_INSECURE_TLS", false),
		MailIMAPHost:   os.Getenv("MAIL_IMAP_HOST"), MailSMTPHost: os.Getenv("MAIL_SMTP_HOST"),
		MailUser: os.Getenv("MAIL_USER"), MailPassword: os.Getenv("MAIL_PASSWORD"),
		BuilderConfig: loadBuilderConfig,
	}
	callbacks, err := security.NewCallbacks(os.Getenv("FLOWOPS_CALLBACK_URLS"))
	if err != nil {
		logger.Error("invalid callback allowlist")
		os.Exit(1)
	}
	ticketAnalyzer := analyzer.New(knowledgeBase).WithBuilderConfig(loadBuilderConfig)
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
	ticketsHandler := handlers.NewTicketsHandler(pool, logger, ticketAnalyzer).WithCallbacks(callbacks).WithBuilderConfig(loadBuilderConfig)
	smartHandler := handlers.NewSmartHandler(knowledgeBase, ticketAnalyzer).WithBuilderConfig(loadBuilderConfig)
	channelContext, stopChannels := context.WithCancel(context.Background())
	defer stopChannels()
	go delivery.New(pool, ticketsHandler.Deliver).Run(channelContext, logger)
	channelStatusUpdate := func(tenant, channel, status, lastError string) {
		entry := map[string]string{"status": status, "last_error": security.SafeChannelError(lastError)}
		_, _ = pool.Exec(context.Background(), `UPDATE app_settings SET channel_metadata=jsonb_set(channel_metadata,ARRAY[$1],COALESCE(channel_metadata->$1,'{}'::jsonb)||$2::jsonb,TRUE) WHERE tenant_id=$3 AND id=TRUE`, channel, mustJSON(entry), tenant)
	}
	channelManager := channels.NewManager(channelContext, ticketsHandler.IngestMessage, logger, channelStatusUpdate).WithEmployeeTickets(ticketsHandler)
	loadTenantChannels := func(ctx context.Context, tenant string) (channels.Config, error) {
		base := channels.Config{TenantID: tenant, MaxAPIURL: os.Getenv("MAX_API_URL"), MaxInsecureTLS: envBool("MAX_INSECURE_TLS", false), BuilderConfig: func(ctx context.Context) (settings.BuilderConfig, error) {
			return integrationsHandler.LoadBuilderConfig(security.WithTenantID(ctx, tenant))
		}}
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT channel_config FROM app_settings WHERE tenant_id=$1 AND id=TRUE`, tenant).Scan(&raw); err != nil && tenant != "default" {
			return base, nil
		}
		values := map[string]string{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &values)
		}
		return channels.ForTenant(tenant, envChannelConfig, base, values), nil
	}
	tenantDispatcher := channels.NewTenantDispatcher(loadTenantChannels)
	ticketsHandler.WithTenantDispatcher(tenantDispatcher)
	rows, err := pool.Query(context.Background(), `SELECT tenant_id FROM workspaces ORDER BY tenant_id`)
	if err != nil {
		logger.Error("failed to load workspaces for channel workers", "error", err)
		os.Exit(1)
	}
	var tenants []string
	for rows.Next() {
		var tenant string
		if rows.Scan(&tenant) == nil {
			tenants = append(tenants, tenant)
		}
	}
	rows.Close()
	for _, tenant := range tenants {
		config, loadErr := loadTenantChannels(context.Background(), tenant)
		if loadErr == nil {
			channelManager.Configure(tenant, config)
		}
	}
	// Seed uniqueness reservations for legacy/default environment credentials so
	// the same account cannot be connected from another tenant.
	defaultConfig, _ := loadTenantChannels(context.Background(), "default")
	for _, entry := range []struct{ channel, token string }{{"telegram", defaultConfig.TelegramToken}, {"max", defaultConfig.MaxToken}} {
		if strings.TrimSpace(entry.token) == "" {
			continue
		}
		identity := channels.ChannelTokenIdentity(entry.channel, entry.token)
		if _, err := pool.Exec(context.Background(), `INSERT INTO channel_bindings(identity,tenant_id,channel) VALUES($1,'default',$2) ON CONFLICT(identity) DO NOTHING`, identity, entry.channel); err != nil {
			logger.Error("failed to reserve default channel identity", "channel", entry.channel, "error", err)
		}
	}
	if parsed, parseErr := mail.ParseAddress(defaultConfig.MailUser); parseErr == nil && strings.TrimSpace(defaultConfig.MailIMAPHost) != "" {
		identity := "mail:" + strings.ToLower(parsed.Address) + "@" + strings.ToLower(strings.TrimSpace(defaultConfig.MailIMAPHost))
		if _, err := pool.Exec(context.Background(), `INSERT INTO channel_bindings(identity,tenant_id,channel) VALUES($1,'default','email') ON CONFLICT(identity) DO NOTHING`, identity); err != nil {
			logger.Error("failed to reserve default email identity", "error", err)
		}
	}
	integrationsHandler.WithChannelRestart(func(tenant string, values map[string]string) {
		config, loadErr := loadTenantChannels(context.Background(), tenant)
		if loadErr == nil {
			// A settings update changes only the channel represented by this save.
			changed := ""
			for _, channel := range []string{"telegram", "max", "email"} {
				keys := map[string][]string{"telegram": {"telegram_token"}, "max": {"max_token"}, "email": {"mail_user"}}
				for _, key := range keys[channel] {
					if _, exists := values[key]; exists {
						changed = channel
						break
					}
				}
			}
			if changed != "" {
				channelManager.ConfigureChannel(tenant, changed, config)
			}
		}
	})
	mux := http.NewServeMux()
	workspaceHandler := security.NewWorkspaceHandler(pool)
	passwordHandler := security.NewPasswordHandler(pool)
	botLinksHandler := security.NewBotLinksHandler(pool)
	mux.HandleFunc("GET /api/v1/profile/bots", botLinksHandler.Status)
	mux.HandleFunc("POST /api/v1/profile/bots/{channel}", botLinksHandler.Change)
	mux.HandleFunc("DELETE /api/v1/profile/bots/{channel}", botLinksHandler.Change)
	mux.HandleFunc("POST /api/v1/auth/login", passwordHandler.Login)
	mux.HandleFunc("POST /api/v1/auth/activate", passwordHandler.Activate)
	mux.HandleFunc("POST /api/v1/auth/logout", passwordHandler.Logout)
	mux.HandleFunc("GET /api/v1/auth/me", workspaceHandler.Me)
	mux.HandleFunc("GET /api/v1/members", workspaceHandler.Members)
	mux.HandleFunc("POST /api/v1/members", workspaceHandler.Members)
	mux.HandleFunc("PATCH /api/v1/members/{id}", workspaceHandler.UpdateMember)
	mux.HandleFunc("POST /api/v1/members/{id}/rotate", workspaceHandler.RotateMember)
	mux.HandleFunc("POST /api/v1/workspaces", workspaceHandler.Create)
	mux.HandleFunc("GET /api/tickets", ticketsHandler.List)
	mux.HandleFunc("GET /api/tickets/{id}", ticketsHandler.Detail)
	mux.HandleFunc("POST /api/tickets", ticketsHandler.Create)
	mux.HandleFunc("POST /api/v1/intake/preview", ticketsHandler.IntakePreview)
	mux.Handle("POST /api/v1/ingest", security.ProtectIngestWithKeys(http.HandlerFunc(ticketsHandler.Ingest), os.Getenv("FLOWOPS_INGEST_TOKEN"), pool))
	mux.Handle("OPTIONS /api/v1/ingest", security.ProtectIngestWithKeys(http.HandlerFunc(ticketsHandler.Ingest), os.Getenv("FLOWOPS_INGEST_TOKEN"), pool))
	mux.HandleFunc("GET /api/v1/keys", integrationsHandler.Keys)
	mux.HandleFunc("POST /api/v1/keys", integrationsHandler.Keys)
	mux.HandleFunc("DELETE /api/v1/keys/{id}", integrationsHandler.RevokeKey)
	mux.HandleFunc("GET /api/v1/widget/config", integrationsHandler.WidgetConfig)
	mux.HandleFunc("PUT /api/v1/widget/config", integrationsHandler.WidgetConfig)
	mux.HandleFunc("OPTIONS /api/v1/widget/config", integrationsHandler.WidgetConfig)
	mux.HandleFunc("POST /api/v1/kb/smart-import", smartHandler.SmartImport)
	mux.HandleFunc("POST /api/v1/settings/preview", smartHandler.Preview)
	mux.HandleFunc("GET /api/v1/settings/builder", integrationsHandler.BuilderSettings)
	mux.HandleFunc("PUT /api/v1/settings/builder", integrationsHandler.BuilderSettings)
	mux.HandleFunc("GET /api/v1/settings/channels", integrationsHandler.ChannelSettings)
	mux.HandleFunc("PUT /api/v1/settings/channels", integrationsHandler.ChannelSettings)
	mux.HandleFunc("DELETE /api/v1/settings/channels/{channel}", integrationsHandler.DeleteChannel)
	mux.HandleFunc("POST /api/v1/settings/channels/{channel}/check", integrationsHandler.CheckChannel)
	mux.Handle("GET /widget.js", noStoreFileServer(http.Dir("web")))
	mux.Handle("GET /", noStoreFileServer(http.Dir("web")))
	mux.Handle("POST /api/tickets/{id}/feedback", security.ProtectIngestWithKeys(http.HandlerFunc(ticketsHandler.Feedback), os.Getenv("FLOWOPS_INGEST_TOKEN"), pool))
	mux.HandleFunc("POST /api/tickets/{id}/reply", ticketsHandler.Reply)
	mux.HandleFunc("POST /api/tickets/{id}/work", ticketsHandler.Work)
	mux.HandleFunc("PATCH /api/tickets/{id}", ticketsHandler.Update)
	mux.HandleFunc("DELETE /api/tickets/reset", ticketsHandler.Reset)
	mux.HandleFunc("GET /api/stats", ticketsHandler.Stats)
	mux.HandleFunc("GET /api/audit", ticketsHandler.Audit)
	mux.HandleFunc("GET /api/knowledge", knowledgeHandler.List)
	mux.HandleFunc("POST /api/knowledge", knowledgeHandler.Create)
	mux.HandleFunc("PATCH /api/knowledge/{filename}", knowledgeHandler.Update)
	mux.HandleFunc("DELETE /api/knowledge/{filename}", knowledgeHandler.Delete)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           loggingMiddleware(logger, security.ProtectOperatorTenants(mux, pool)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      45 * time.Second,
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

func applyChannelConfig(config channels.Config, values map[string]string) channels.Config {
	if value, ok := values["telegram_token"]; ok {
		config.TelegramToken = value
	}
	if value, ok := values["max_token"]; ok {
		config.MaxToken = value
	}
	if value, ok := values["mail_imap_host"]; ok {
		config.MailIMAPHost = value
	}
	if value, ok := values["mail_smtp_host"]; ok {
		config.MailSMTPHost = value
	}
	if value, ok := values["mail_user"]; ok {
		config.MailUser = value
	}
	if value, ok := values["mail_password"]; ok {
		config.MailPassword = value
	}
	return config
}

func mustJSON(value any) string { data, _ := json.Marshal(value); return string(data) }

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
