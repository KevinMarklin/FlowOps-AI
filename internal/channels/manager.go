package channels

import (
	"context"
	"strings"
	"sync"

	"flowops-ai/internal/settings"
)

type ChannelManager struct {
	root            context.Context
	ingest          IngestFunc
	employeeTickets EmployeeTickets
	logger          interface {
		Info(string, ...any)
		Error(string, ...any)
	}
	statusUpdate func(string, string, string, string)
	mu           sync.Mutex
	workers      map[string]context.CancelFunc
}

func NewManager(root context.Context, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}, statusUpdates ...func(string, string, string, string)) *ChannelManager {
	m := &ChannelManager{root: root, ingest: ingest, logger: logger, workers: make(map[string]context.CancelFunc)}
	if len(statusUpdates) > 0 {
		m.statusUpdate = statusUpdates[0]
	}
	return m
}

func workerKey(tenant, channel string) string { return tenant + "\x00" + channel }

func (m *ChannelManager) WithEmployeeTickets(service EmployeeTickets) *ChannelManager {
	m.employeeTickets = service
	return m
}

// Configure replaces only the workers belonging to tenant. Each channel has
// its own cancellation scope so one channel can be stopped independently.
func (m *ChannelManager) Configure(tenant string, cfg Config) {
	if strings.TrimSpace(tenant) == "" {
		tenant = "default"
	}
	cfg.TenantID = tenant
	if m.statusUpdate != nil {
		cfg.StatusUpdate = func(channel, status, lastError string) { m.statusUpdate(tenant, channel, status, lastError) }
	}
	m.StopTenant(tenant)
	if cfg.BuilderConfig == nil {
		cfg.BuilderConfig = func(context.Context) (settings.BuilderConfig, error) { return settings.DefaultBuilderConfig(), nil }
	}
	if strings.TrimSpace(cfg.TelegramToken) != "" {
		m.start(tenant, "telegram", func(ctx context.Context) {
			runTelegram(ctx, tenant, cfg.TelegramToken, cfg.BuilderConfig, cfg.StatusUpdate, m.ingest, m.logger, m.employeeTickets)
		})
	}
	if strings.TrimSpace(cfg.MaxToken) != "" {
		m.start(tenant, "max", func(ctx context.Context) { runMax(ctx, cfg, m.ingest, m.logger, m.employeeTickets) })
	}
	if cfg.emailEnabled() {
		m.start(tenant, "email", func(ctx context.Context) { runEmail(ctx, cfg, m.ingest, m.logger) })
	}
}

func (m *ChannelManager) ConfigureChannel(tenant, channel string, cfg Config) {
	if strings.TrimSpace(tenant) == "" {
		tenant = "default"
	}
	cfg.TenantID = tenant
	if m.statusUpdate != nil {
		cfg.StatusUpdate = func(ch, status, lastError string) { m.statusUpdate(tenant, ch, status, lastError) }
	}
	m.Stop(tenant, channel)
	if cfg.BuilderConfig == nil {
		cfg.BuilderConfig = func(context.Context) (settings.BuilderConfig, error) { return settings.DefaultBuilderConfig(), nil }
	}
	switch channel {
	case "telegram":
		if strings.TrimSpace(cfg.TelegramToken) != "" {
			m.start(tenant, channel, func(ctx context.Context) {
				runTelegram(ctx, tenant, cfg.TelegramToken, cfg.BuilderConfig, cfg.StatusUpdate, m.ingest, m.logger, m.employeeTickets)
			})
		}
	case "max":
		if strings.TrimSpace(cfg.MaxToken) != "" {
			m.start(tenant, channel, func(ctx context.Context) { runMax(ctx, cfg, m.ingest, m.logger, m.employeeTickets) })
		}
	case "email":
		if cfg.emailEnabled() {
			m.start(tenant, channel, func(ctx context.Context) { runEmail(ctx, cfg, m.ingest, m.logger) })
		}
	}
}

func (m *ChannelManager) start(tenant, channel string, run func(context.Context)) {
	ctx, cancel := context.WithCancel(m.root)
	m.mu.Lock()
	m.workers[workerKey(tenant, channel)] = cancel
	m.mu.Unlock()
	if m.logger != nil {
		m.logger.Info("channel status", "tenant_id", tenant, "channel", channel, "status", "starting")
	}
	go run(ctx)
}

func (m *ChannelManager) Stop(tenant, channel string) {
	m.mu.Lock()
	cancel := m.workers[workerKey(tenant, channel)]
	delete(m.workers, workerKey(tenant, channel))
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *ChannelManager) StopTenant(tenant string) {
	for _, channel := range []string{"telegram", "max", "email"} {
		m.Stop(tenant, channel)
	}
}

func (m *ChannelManager) StopAll() {
	m.mu.Lock()
	workers := m.workers
	m.workers = make(map[string]context.CancelFunc)
	m.mu.Unlock()
	for _, cancel := range workers {
		cancel()
	}
}
