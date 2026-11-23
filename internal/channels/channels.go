package channels

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"flowops-ai/internal/models"
)

type Config struct {
	TelegramToken  string
	MaxToken       string
	MaxAPIURL      string
	MaxInsecureTLS bool
	MailIMAPHost   string
	MailSMTPHost   string
	MailUser       string
	MailPassword   string
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
	// Certificate validation is mandatory even if legacy configuration requests a bypass.
	transport.DisableKeepAlives = true
	client := &http.Client{Transport: transport, Timeout: 40 * time.Second, CheckRedirect: rejectChannelRedirect}
	actual, _ := maxClients.LoadOrStore(c.MaxInsecureTLS, client)
	return actual.(*http.Client)
}

func Start(ctx context.Context, cfg Config, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) {
	if strings.TrimSpace(cfg.TelegramToken) == "" {
		logger.Info("channel status", "channel", "Telegram", "status", "disabled")
	} else {
		logger.Info("channel status", "channel", "Telegram", "status", "starting")
		go runTelegram(ctx, cfg.TelegramToken, ingest, logger)
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
