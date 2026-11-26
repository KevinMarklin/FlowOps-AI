package channels

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"sync"
	"time"

	"flowops-ai/internal/models"
	"flowops-ai/internal/settings"
)

type Config struct {
	TenantID       string
	StatusUpdate   func(string, string, string)
	TelegramToken  string
	MaxToken       string
	MaxAPIURL      string
	MaxInsecureTLS bool
	MailIMAPHost   string
	MailSMTPHost   string
	MailUser       string
	MailPassword   string
	BuilderConfig  settings.Loader
}

// ForTenant applies saved workspace settings on top of defaults, allowing
// process environment credentials only for the legacy default workspace.
func ForTenant(tenant string, envConfig, emptyConfig Config, saved map[string]string) Config {
	config := emptyConfig
	if tenant == "default" {
		config = envConfig
	}
	config.TenantID = tenant
	if value, ok := saved["telegram_token"]; ok {
		config.TelegramToken = value
	}
	if value, ok := saved["max_token"]; ok {
		config.MaxToken = value
	}
	if value, ok := saved["mail_imap_host"]; ok {
		config.MailIMAPHost = value
	}
	if value, ok := saved["mail_smtp_host"]; ok {
		config.MailSMTPHost = value
	}
	if value, ok := saved["mail_user"]; ok {
		config.MailUser = value
	}
	if value, ok := saved["mail_password"]; ok {
		config.MailPassword = value
	}
	return config
}

func (c Config) updateStatus(channel, status, lastError string) {
	if c.StatusUpdate != nil {
		c.StatusUpdate(channel, status, lastError)
	}
}

func (c Config) builderConfig(ctx context.Context) settings.BuilderConfig {
	return settings.LoadOrDefault(ctx, c.BuilderConfig)
}

func StartReplyText(ctx context.Context, loader settings.Loader) string {
	config := settings.LoadOrDefault(ctx, loader)
	if strings.TrimSpace(config.StartTemplate) == "" {
		return settings.DefaultBuilderConfig().StartTemplate
	}
	return config.StartTemplate
}

type IngestResult struct {
	TicketID         int      `json:"ticket_id"`
	AutoReplied      bool     `json:"auto_replied"`
	ReplyText        string   `json:"reply_text"`
	Channel          string   `json:"channel"`
	Department       string   `json:"department"`
	Priority         string   `json:"priority"`
	Status           string   `json:"status"`
	ExtractedSummary *string  `json:"extracted_summary"`
	PIIMaskedCount   int      `json:"pii_masked_count"`
	PIIMaskedTypes   []string `json:"pii_masked_types"`
}

type IngestFunc func(context.Context, models.IngestRequest) (IngestResult, error)

func (c Config) emailEnabled() bool {
	return strings.TrimSpace(c.MailIMAPHost) != "" && strings.TrimSpace(c.MailSMTPHost) != "" && strings.TrimSpace(c.MailUser) != ""
}

func (c Config) maxURL() string {
	if strings.TrimSpace(c.MaxAPIURL) == "" {
		return "https://platform-api2.max.ru"
	}
	return strings.TrimRight(c.MaxAPIURL, "/")
}

var maxClients sync.Map

func (c Config) maxHTTPClient() *http.Client {
	if client, ok := maxClients.Load(c.MaxInsecureTLS); ok {
		return client.(*http.Client)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if c.MaxInsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // #nosec G402 -- explicit MAX_INSECURE_TLS opt-in
	}
	transport.DisableKeepAlives = true
	client := &http.Client{Transport: transport, Timeout: 40 * time.Second, CheckRedirect: rejectChannelRedirect}
	actual, _ := maxClients.LoadOrStore(c.MaxInsecureTLS, client)
	return actual.(*http.Client)
}

func Start(ctx context.Context, cfg Config, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) {
	if cfg.TenantID == "" {
		cfg.TenantID = "default"
	}
	if cfg.BuilderConfig == nil {
		cfg.BuilderConfig = func(context.Context) (settings.BuilderConfig, error) { return settings.DefaultBuilderConfig(), nil }
	}
	if strings.TrimSpace(cfg.TelegramToken) == "" {
		logger.Info("channel status", "channel", "Telegram", "status", "disabled")
	} else {
		logger.Info("channel status", "channel", "Telegram", "status", "starting")
		go runTelegram(ctx, cfg.TenantID, cfg.TelegramToken, cfg.BuilderConfig, cfg.StatusUpdate, ingest, logger)
	}
	if strings.TrimSpace(cfg.MaxToken) == "" {
		logger.Info("channel status", "channel", "MAX", "status", "disabled")
	} else {
		logger.Info("channel status", "channel", "MAX", "status", "starting")
		go runMax(ctx, cfg, ingest, logger)
	}
	if !cfg.emailEnabled() {
		logger.Info("channel status", "channel", "Email", "status", "disabled")
	} else {
		logger.Info("channel status", "channel", "Email", "status", "starting")
		go runEmail(ctx, cfg, ingest, logger)
	}
}
