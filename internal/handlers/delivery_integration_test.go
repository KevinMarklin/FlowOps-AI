package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/channels"
	"flowops-ai/internal/database"
	"flowops-ai/internal/delivery"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func replyTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("FLOWOPS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated flowops_security_test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "flowops_security_test" {
		t.Fatal("dedicated flowops_security_test database required")
	}
	admin, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flowops_handler_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(context.Background(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if strings.HasPrefix(schema, "flowops_handler_test_") {
			_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		}
		admin.Close()
	})
	if err = database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTenantIsolationTicketsAndSettings(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	tokens := map[string]string{"tenant_a": strings.Repeat("a", 40), "tenant_b": strings.Repeat("b", 40)}
	for tenant, token := range tokens {
		hash := sha256.Sum256([]byte(token))
		if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES($1,$1,$2,$3)`, tenant, token[:8], hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO app_settings(tenant_id,id,builder_config) VALUES($1,TRUE,$2::jsonb) ON CONFLICT (tenant_id,id) DO UPDATE SET builder_config=EXCLUDED.builder_config`, tenant, `{"departments":[{"name":"`+tenant+`","description":""}],"tone":"friendly"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO tickets(tenant_id,user_email,raw_text) VALUES($1,$2,'private request')`, tenant, tenant+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	tickets := &TicketsHandler{db: db}
	integrations := NewIntegrationsHandler(db)
	mux.HandleFunc("GET /api/tickets", tickets.List)
	mux.HandleFunc("GET /api/v1/settings/builder", integrations.BuilderSettings)
	server := security.ProtectOperatorTenants(mux, db)
	for tenant, token := range tokens {
		t.Run(tenant, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/tickets", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("ticket list status=%d body=%s", response.Code, response.Body.String())
			}
			var rows []map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0]["user_email"] != tenant+"@example.test" {
				t.Fatalf("tenant ticket leak: %#v", rows)
			}

			request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/builder", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response = httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("settings status=%d body=%s", response.Code, response.Body.String())
			}
			var config map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
				t.Fatal(err)
			}
			departments := config["departments"].([]any)
			if departments[0].(map[string]any)["name"] != tenant {
				t.Fatalf("tenant settings leak: %#v", config)
			}
		})
	}
}

func TestChannelIdentityConflictAndTenantWorkerIsolation(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	t.Setenv("FLOWOPS_OPERATOR_TOKEN", strings.Repeat("z", 32))
	for _, tenant := range []string{"channel_a", "channel_b", "channel_c"} {
		token := strings.Repeat(string(tenant[len(tenant)-1]), 40)
		hash := sha256.Sum256([]byte(token))
		if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES($1,$1,$2,$3)`, tenant, token[:8], hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO app_settings(tenant_id,id) VALUES($1,TRUE)`, tenant); err != nil {
			t.Fatal(err)
		}
	}
	first := NewIntegrationsHandler(db).WithChannelValidator(func(_ context.Context, channel string, values map[string]string) (string, string, map[string]string, error) {
		return "tg:87654321", "@FlowOpsAI2_bot", map[string]string{"bot_username": "FlowOpsAI2_bot", "bot_name": "FlowOps"}, nil
	})
	put := func(tenant, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/settings/channels", strings.NewReader(`{"telegram_token":"`+token+`"}`))
		r = r.WithContext(security.WithTenantID(r.Context(), tenant))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("z", 32))
		w := httptest.NewRecorder()
		first.ChannelSettings(w, r)
		return w
	}
	response := put("channel_a", "fake-but-validated")
	if response.Code != http.StatusOK {
		t.Fatalf("first channel bind status=%d: %s", response.Code, response.Body.String())
	}
	response = put("channel_b", "same-fake-token")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "@FlowOpsAI2_bot") {
		t.Fatalf("duplicate bind status=%d body=%s", response.Code, response.Body.String())
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/channels/telegram", nil)
	deleteRequest.SetPathValue("channel", "telegram")
	deleteRequest = deleteRequest.WithContext(security.WithTenantID(deleteRequest.Context(), "channel_a"))
	deleteRequest.Header.Set("Authorization", "Bearer "+strings.Repeat("z", 32))
	deleteResponse := httptest.NewRecorder()
	first.DeleteChannel(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete channel status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	response = put("channel_b", "same-fake-token")
	if response.Code != http.StatusOK {
		t.Fatalf("channel binding was not released: %d %s", response.Code, response.Body.String())
	}

	mailHandler := NewIntegrationsHandler(db).WithChannelValidator(func(_ context.Context, channel string, values map[string]string) (string, string, map[string]string, error) {
		return "mail:" + strings.ToLower(values["mail_user"]) + "@" + strings.ToLower(values["mail_imap_host"]), values["mail_user"], map[string]string{"email_user": values["mail_user"]}, nil
	})
	mailPut := func(tenant string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/settings/channels", strings.NewReader(`{"mail_imap_host":"imap.example.test","mail_smtp_host":"smtp.example.test","mail_user":"Support@Example.test","mail_password":"secret"}`))
		r = r.WithContext(security.WithTenantID(r.Context(), tenant))
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("z", 32))
		w := httptest.NewRecorder()
		mailHandler.ChannelSettings(w, r)
		return w
	}
	if res := mailPut("channel_a"); res.Code != http.StatusOK {
		t.Fatalf("first email bind status=%d body=%s", res.Code, res.Body.String())
	}
	if res := mailPut("channel_b"); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "support@example.test") {
		t.Fatalf("duplicate email bind status=%d body=%s", res.Code, res.Body.String())
	}

	envConfig := channels.Config{TelegramToken: "env-token", MaxToken: "env-max", MailUser: "default@example.test", MailIMAPHost: "imap", MailSMTPHost: "smtp"}
	other := channels.ForTenant("channel_b", envConfig, channels.Config{TenantID: "channel_b"}, nil)
	if other.TelegramToken != "" || other.MaxToken != "" || other.MailUser != "" {
		t.Fatalf("non-default inherited env channel config: %+v", other)
	}
	defaultConfig := channels.ForTenant("default", envConfig, channels.Config{TenantID: "default"}, nil)
	if defaultConfig.TelegramToken != "env-token" {
		t.Fatalf("default env fallback lost: %+v", defaultConfig)
	}

	knowledgeRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(knowledgeRoot, "default"), 0700); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(filepath.Join(knowledgeRoot, "default"))
	if err != nil {
		t.Fatal(err)
	}
	kbA, err := base.TenantBase("channel_a")
	if err != nil {
		t.Fatal(err)
	}
	kbB, err := base.TenantBase("channel_b")
	if err != nil {
		t.Fatal(err)
	}
	tenantA := security.WithTenantID(context.Background(), "channel_a")
	tenantB := security.WithTenantID(context.Background(), "channel_b")
	loader := func(ctx context.Context) (settings.BuilderConfig, error) {
		var raw []byte
		err := db.QueryRow(ctx, `SELECT builder_config FROM app_settings WHERE tenant_id=$1 AND id=TRUE`, security.TenantID(ctx)).Scan(&raw)
		return settings.LoadOrDefault(ctx, func(context.Context) (settings.BuilderConfig, error) {
			var c settings.BuilderConfig
			_ = json.Unmarshal(raw, &c)
			return c, nil
		}), err
	}
	for tenant, start := range map[string]string{"channel_a": "A start", "channel_b": "B start"} {
		data, _ := json.Marshal(settings.BuilderConfig{StartTemplate: start, Departments: []settings.Department{{Name: tenant}}})
		if _, err := db.Exec(ctx, `UPDATE app_settings SET builder_config=$1::jsonb WHERE tenant_id=$2`, string(data), tenant); err != nil {
			t.Fatal(err)
		}
	}
	startA := channels.StartReplyText(tenantA, loader)
	startB := channels.StartReplyText(tenantB, loader)
	if startA != "A start" || startB != "B start" {
		t.Fatalf("/start settings crossed tenants: %q / %q", startA, startB)
	}
	if _, err = kbA.Add("IT", "Tenant A secret Wi-Fi", []string{"tenantawifi"}, "Private tenant A access instructions"); err != nil {
		t.Fatal(err)
	}
	ticketAnalyzer := analyzer.New(base).WithBuilderConfig(loader)
	resultA := ticketAnalyzer.Analyze(tenantA, "tenantawifi")
	resultB := ticketAnalyzer.Analyze(tenantB, "tenantawifi")
	if resultA.RAGAnswer == nil || resultB.RAGAnswer != nil {
		t.Fatalf("tenant knowledge crossed: A=%v B=%v", resultA.RAGAnswer, resultB.RAGAnswer)
	}
	ticketHandler := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), ticketAnalyzer).WithBuilderConfig(loader)
	if _, err = ticketHandler.IngestMessage(tenantA, models.IngestRequest{Channel: "bot", Sender: "tg:10", Message: "request A"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ticketHandler.IngestMessage(tenantB, models.IngestRequest{Channel: "bot", Sender: "tg:10", Message: "request B"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE tenant_id=$1`, security.TenantID(tenantA)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ticket tenant isolation count=%d err=%v", count, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE tenant_id=$1`, security.TenantID(tenantB)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("second tenant ticket isolation count=%d err=%v", count, err)
	}
	if len(kbA.Documents()) == 0 || len(kbB.Documents()) != 0 {
		t.Fatalf("knowledge bases are not isolated: A=%d B=%d", len(kbA.Documents()), len(kbB.Documents()))
	}
}

func TestSavedBuilderSettingsChangeLiveRepliesAndAnalyzerPrompt(t *testing.T) {
	t.Setenv("FLOWOPS_OPERATOR_TOKEN", strings.Repeat("b", 32))
	db := replyTestDB(t)
	api := NewIntegrationsHandler(db)
	put := func() {
		t.Helper()
		body := `{"departments":[{"name":"Custom Ops","description":"Управляет корпоративными принтерами и печатью"}],"tone":"formal","start_template":"Добрый день, настройка обновлена.","handoff_template":"Обращение {ticket_id} передано в {department}.","signature":"Команда Custom Ops"}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/builder", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("b", 32))
		res := httptest.NewRecorder()
		api.BuilderSettings(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("save builder config: %d %s", res.Code, res.Body.String())
		}
	}
	put()
	loader := func(ctx context.Context) (settings.BuilderConfig, error) { return api.LoadBuilderConfig(ctx) }
	if got := channels.StartReplyText(context.Background(), loader); got != "Добрый день, настройка обновлена." {
		t.Fatalf("start reply = %q", got)
	}
	config, err := loader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := channels.BuilderEscalationReplyText(45, "Custom Ops", config.HandoffTemplate); got != "Обращение #45 передано в Custom Ops." {
		t.Fatalf("handoff reply = %q", got)
	}
	if got := channels.BuilderEscalationReplyText(45, "Unassigned", config.HandoffTemplate); got != "Обращение #45 передано в дежурный отдел." {
		t.Fatalf("unassigned handoff reply = %q", got)
	}
	if got := channels.AppendSignature("Ответ базы знаний", config.Signature); got != "Ответ базы знаний\n\nКоманда Custom Ops" {
		t.Fatalf("signed answer = %q", got)
	}
	if got := channels.FormatTelegramHTML(channels.AppendSignature("Ответ базы знаний", config.Signature)); !strings.Contains(got, "Команда Custom Ops") {
		t.Fatalf("telegram signature missing: %q", got)
	}
	if got := channels.FormatMAX(channels.AppendSignature("Ответ базы знаний", config.Signature)); !strings.Contains(got, "Команда Custom Ops") {
		t.Fatalf("MAX signature missing: %q", got)
	}
	if got := channels.FormatEmailHTML(45, "Вопрос", channels.AppendSignature("Ответ базы знаний", config.Signature), true); !strings.Contains(got, "Команда Custom Ops") {
		t.Fatalf("email signature missing: %q", got)
	}
	settingsForPrompt := settings.LoadOrDefault(context.Background(), loader)
	prompt := analyzer.ClassificationPolicy(settingsForPrompt)
	if !strings.Contains(prompt, "Custom Ops: Управляет корпоративными принтерами и печатью") || !strings.Contains(prompt, "официальный") {
		t.Fatalf("builder settings missing from analyzer prompt: %q", prompt)
	}
	if _, err := analyzer.DecodeModelResultForTest(`{"department":"Custom Ops","priority":"medium","extracted_summary":"Заявка на печать"}`, settingsForPrompt.Departments); err != nil {
		t.Fatalf("configured department rejected by model contract: %v", err)
	}
}

func TestAPIKeyIssueRevokeAndIngestAuthorization(t *testing.T) {
	t.Setenv("FLOWOPS_OPERATOR_TOKEN", strings.Repeat("k", 32))
	db := replyTestDB(t)
	handler := NewIntegrationsHandler(db)
	request := httptest.NewRequest("POST", "/api/v1/keys", strings.NewReader(`{"name":"test CRM","scope":"ingest"}`))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("k", 32))
	response := httptest.NewRecorder()
	handler.Keys(response, request)
	if response.Code != 201 {
		t.Fatalf("issue key: %d %s", response.Code, response.Body.String())
	}
	var issued struct {
		ID     int    `json:"id"`
		Key    string `json:"key"`
		Prefix string `json:"key_prefix"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.ID < 1 || !strings.HasPrefix(issued.Key, "flw_") || !strings.HasPrefix(issued.Key, issued.Prefix) {
		t.Fatalf("invalid issued key: %+v", issued)
	}
	sum := sha256.Sum256([]byte(issued.Key))
	var stored string
	if err := db.QueryRow(context.Background(), `SELECT key_hash FROM api_keys WHERE id=$1`, issued.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != hex.EncodeToString(sum[:]) || stored == issued.Key {
		t.Fatal("database must store only the SHA-256 key hash")
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	protected := security.ProtectIngestWithKeys(next, "", db)
	ingest := httptest.NewRequest("POST", "/api/v1/ingest", strings.NewReader(`{"channel":"api","message":"test"}`))
	ingest.Header.Set("Authorization", "Bearer "+issued.Key)
	response = httptest.NewRecorder()
	protected.ServeHTTP(response, ingest)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid DB key not accepted: %d %s", response.Code, response.Body.String())
	}
	var lastUsed *time.Time
	if err := db.QueryRow(context.Background(), `SELECT last_used_at FROM api_keys WHERE id=$1`, issued.ID).Scan(&lastUsed); err != nil || lastUsed == nil {
		t.Fatalf("last_used_at not updated: %v %v", lastUsed, err)
	}
	revoke := httptest.NewRequest("DELETE", "/api/v1/keys/"+strconv.Itoa(issued.ID), nil)
	revoke.Header.Set("Authorization", "Bearer "+strings.Repeat("k", 32))
	revoke.SetPathValue("id", strconv.Itoa(issued.ID))
	response = httptest.NewRecorder()
	handler.RevokeKey(response, revoke)
	if response.Code != http.StatusNoContent {
		t.Fatalf("revoke key: %d %s", response.Code, response.Body.String())
	}
	ingest = httptest.NewRequest("POST", "/api/v1/ingest", strings.NewReader(`{"channel":"api","message":"test"}`))
	ingest.Header.Set("Authorization", "Bearer "+issued.Key)
	response = httptest.NewRecorder()
	protected.ServeHTTP(response, ingest)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key still accepted: %d", response.Code)
	}
}

func TestIssuedAPIKeyAuthorizesIngestEndpoint(t *testing.T) {
	t.Setenv("FLOWOPS_OPERATOR_TOKEN", strings.Repeat("k", 32))
	db := replyTestDB(t)
	keyHandler := NewIntegrationsHandler(db)
	issuedResponse := httptest.NewRecorder()
	issueRequest := httptest.NewRequest("POST", "/api/v1/keys", strings.NewReader(`{"name":"endpoint test","scope":"ingest"}`))
	issueRequest.Header.Set("Authorization", "Bearer "+strings.Repeat("k", 32))
	keyHandler.Keys(issuedResponse, issueRequest)
	if issuedResponse.Code != http.StatusCreated {
		t.Fatalf("issue key: %d %s", issuedResponse.Code, issuedResponse.Body.String())
	}
	var issued struct {
		ID  int    `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(issuedResponse.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load("../../knowledge_base")
	if err != nil {
		t.Fatal(err)
	}
	callbacks, _ := security.NewCallbacks("")
	tickets := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base)).WithCallbacks(callbacks)
	endpoint := security.ProtectIngestWithKeys(http.HandlerFunc(tickets.Ingest), "", db)
	request := httptest.NewRequest("POST", "/api/v1/ingest", strings.NewReader(`{"channel":"api","sender":"crm@example.test","message":"The office VPN is unavailable"}`))
	request.Header.Set("Authorization", "Bearer "+issued.Key)
	response := httptest.NewRecorder()
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("ingest endpoint rejected issued key: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		TicketID int `json:"ticket_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.TicketID < 1 {
		t.Fatalf("ingest did not create ticket: %+v %v", result, err)
	}
}

func TestReplyAndPatchUseAtomicDeliveryQueue(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	var id int
	if err := db.QueryRow(ctx, `INSERT INTO tickets(raw_text,channel,user_email,callback_url) VALUES('synthetic','email','employee@example.test','https://hooks.example.com/reply') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	policy, _ := security.NewCallbacks("https://hooks.example.com/reply")
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), nil).WithCallbacks(policy)
	r := httptest.NewRequest("POST", "/api/tickets/"+strconv.Itoa(id)+"/reply", strings.NewReader(`{"operator_reply":"synthetic answer"}`))
	r.SetPathValue("id", strconv.Itoa(id))
	w := httptest.NewRecorder()
	h.Reply(w, r)
	if w.Code != 200 {
		t.Fatalf("reply failed: %d %s", w.Code, w.Body.String())
	}
	var result models.Ticket
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "resolved" || result.DeliveryStatus != "pending" {
		t.Fatalf("closed before delivery: %+v", result)
	}
	r = httptest.NewRequest("PATCH", "/api/tickets/"+strconv.Itoa(id), strings.NewReader(`{"operator_reply":"synthetic answer","priority":"high"}`))
	r.SetPathValue("id", strconv.Itoa(id))
	w = httptest.NewRecorder()
	h.Update(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var count int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM ticket_deliveries`).Scan(&count)
	if count != 2 {
		t.Fatalf("duplicate events: %d", count)
	}
	q := delivery.New(db, func(context.Context, delivery.Message) error { return nil })
	for i := 0; i < 2; i++ {
		if _, err := q.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	w = httptest.NewRecorder()
	h.Detail(w, r)
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "closed" || result.DeliveryStatus != "sent" || result.Priority != "high" {
		t.Fatalf("unexpected delivery result: %+v", result)
	}
}

func TestReplyAuditFailureRollsBackTicketAndEvents(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	var id int
	_ = db.QueryRow(ctx, `INSERT INTO tickets(raw_text,channel,user_email) VALUES('synthetic','email','employee@example.test') RETURNING id`).Scan(&id)
	if _, err := db.Exec(ctx, `ALTER TABLE audit_logs ADD CONSTRAINT fail_operator_audit CHECK(event_type<>'OPERATOR_REPLY')`); err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	r := httptest.NewRequest("POST", "/api/tickets/"+strconv.Itoa(id)+"/reply", strings.NewReader(`{"operator_reply":"synthetic answer"}`))
	r.SetPathValue("id", strconv.Itoa(id))
	w := httptest.NewRecorder()
	h.Reply(w, r)
	if w.Code != 500 {
		t.Fatalf("expected atomic failure: %d", w.Code)
	}
	var reply *string
	var count int
	_ = db.QueryRow(ctx, `SELECT operator_reply FROM tickets WHERE id=$1`, id).Scan(&reply)
	_ = db.QueryRow(ctx, `SELECT count(*) FROM ticket_deliveries`).Scan(&count)
	if reply != nil || count != 0 {
		t.Fatal("failed audit left ticket reply or outgoing events")
	}
}
