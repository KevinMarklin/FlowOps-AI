package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

func TestConcurrentCreateReusesTicketAndModelResult(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('reliable','Synthetic','x','reliable-hash')`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"department\":\"IT\",\"priority\":\"medium\",\"extracted_summary\":\"Сбой VPN\"}"}}]}`)
	}))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
	t.Setenv("FLOWOPS_AI_PROVIDERS", "compatible")
	t.Setenv("AI_API_KEY", "synthetic")
	t.Setenv("AI_BASE_URL", server.URL)
	t.Setenv("GEMINI_API_KEY", "")
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	p := security.Principal{ID: "person-a", TenantID: "reliable", Email: "synthetic@example.org", Role: "employee"}
	create := func(key, text string, principal security.Principal) *httptest.ResponseRecorder {
		body, _ := json.Marshal(models.CreateTicketRequest{RawText: text, UserEmail: principal.Email})
		r := httptest.NewRequest("POST", "/api/tickets", strings.NewReader(string(body)))
		r.Header.Set("Idempotency-Key", key)
		r = r.WithContext(security.WithPrincipal(ctx, principal))
		w := httptest.NewRecorder()
		h.Create(w, r)
		return w
	}
	const concurrent = 16
	results := make(chan *httptest.ResponseRecorder, concurrent)
	var wg sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- create("request-42", "VPN connection failed", p) }()
	}
	wg.Wait()
	close(results)
	ids := map[int]bool{}
	replays := 0
	for w := range results {
		var ticket models.Ticket
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &ticket) != nil {
			t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
		}
		ids[ticket.ID] = true
		if w.Header().Get("Idempotency-Replayed") == "true" {
			replays++
		}
	}
	if len(ids) != 1 || calls.Load() != 1 || replays != concurrent-1 {
		t.Fatalf("duplicate creation: tickets=%d model_calls=%d replays=%d", len(ids), calls.Load(), replays)
	}
	if w := create("request-42", "Different problem", p); w.Code != 409 {
		t.Fatal("key accepted changed payload")
	}
	if w := create("bad key", "Different problem", p); w.Code != 400 {
		t.Fatal("invalid key accepted")
	}
	other := p
	other.ID = "person-b"
	other.Email = "other@example.org"
	if w := create("request-42", "VPN connection failed", other); w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal("identity scope leaked")
	}
	// Failure after INSERT must roll back both the ticket and the key.
	_, err := db.Exec(ctx, `CREATE FUNCTION refuse_request() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER refuse_request BEFORE INSERT ON ticket_requests FOR EACH ROW EXECUTE FUNCTION refuse_request()`)
	if err != nil {
		t.Fatal(err)
	}
	if w := create("rollback-key", "VPN rollback test", p); w.Code != 503 {
		t.Fatal("transaction failure not reported")
	}
	var count int
	if db.QueryRow(ctx, `SELECT COUNT(*) FROM tickets WHERE tenant_id='reliable'`).Scan(&count) != nil || count != 2 {
		t.Fatal("ticket persisted without request key")
	}
	if _, err := db.Exec(ctx, `DROP TRIGGER refuse_request ON ticket_requests`); err != nil {
		t.Fatal(err)
	}
	if w := create("rollback-key", "VPN rollback test", p); w.Code != 201 {
		t.Fatal("retry after rollback failed")
	}
}

func TestBotRequestKeyCannotFollowAccountRebinding(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithTenantID(context.Background(), "default")
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('rebound','Synthetic','x','rebound-hash');INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('rebound-person','rebound','Synthetic','synthetic@example.org','employee','member-hash')`); err != nil {
		t.Fatal(err)
	}
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	request := models.IngestRequest{Channel: "bot", Sender: "tg:17", Message: "VPN issue", IdempotencyKey: "tg-message:7"}
	first, err := h.IngestMessage(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := h.IngestMessage(ctx, request)
	if err != nil || replay.TicketID != first.TicketID {
		t.Fatal("bot replay created a second ticket")
	}
	request.Message = "Different issue"
	if _, err := h.IngestMessage(ctx, request); !errors.Is(err, errRequestConflict) {
		t.Fatal("bot key accepted changed content")
	}
	request.Message = "VPN issue"
	code := "link_" + strings.Repeat("x", 43)
	hash := sha256.Sum256([]byte(code))
	if _, err := db.Exec(ctx, `INSERT INTO bot_link_codes(token_hash,member_id,channel,auth_version,expires_at) VALUES($1,'rebound-person','telegram',0,NOW()+INTERVAL '10 minutes')`, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	if err := h.LinkBot(ctx, "tg:17", code); err != nil {
		t.Fatal(err)
	}
	if _, err := h.IngestMessage(ctx, request); !errors.Is(err, errRequestConflict) {
		t.Fatal("old message recreated under new account")
	}
	var count int
	if db.QueryRow(ctx, `SELECT COUNT(*) FROM tickets WHERE tenant_id='rebound'`).Scan(&count) != nil || count != 0 {
		t.Fatal("old message leaked to new company")
	}
}
