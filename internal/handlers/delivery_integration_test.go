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
