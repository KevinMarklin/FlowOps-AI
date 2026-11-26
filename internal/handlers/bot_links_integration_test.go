package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/channels"
	"flowops-ai/internal/delivery"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

func TestPersonalBotLinksOwnershipExpiryAndSharedHistory(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	hash := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	for _, tenant := range []string{"link-a", "link-b"} {
		if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES($1,$1,'fixture',$2)`, tenant, hash(tenant)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"link-person", "link-other", "link-admin"} {
		role := "employee"
		if id == "link-admin" {
			role = "admin"
		}
		if _, err := db.Exec(ctx, `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES($1,'link-a',$1,$2,$3,$4)`, id, id+"@example.org", role, hash(id)); err != nil {
			t.Fatal(err)
		}
	}
	p := security.Principal{ID: "link-person", TenantID: "link-a", Role: "employee", Email: "link-person@example.org"}
	profile := security.NewBotLinksHandler(db)
	change := func(method, channel string, principal security.Principal) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/profile/bots/"+channel, strings.NewReader("{}"))
		r.SetPathValue("channel", channel)
		r = r.WithContext(security.WithPrincipal(ctx, principal))
		w := httptest.NewRecorder()
		profile.Change(w, r)
		return w
	}
	issue := func(channel string) string {
		t.Helper()
		w := change("POST", channel, p)
		if w.Code != 201 {
			t.Fatalf("code issue status=%d", w.Code)
		}
		var value struct {
			Code    string    `json:"code"`
			Expires time.Time `json:"expires_at"`
		}
		if json.Unmarshal(w.Body.Bytes(), &value) != nil || len(value.Code) != 48 || time.Until(value.Expires) > 10*time.Minute || time.Until(value.Expires) < 9*time.Minute {
			t.Fatal("invalid expiry or format")
		}
		return value.Code
	}
	bot := security.WithTenantID(ctx, "default")
	first := issue("telegram")
	code := issue("telegram")
	if security.LinkBotAccount(bot, db, "tg:17", first) == nil {
		t.Fatal("replaced code accepted")
	}
	if security.LinkBotAccount(bot, db, "max:17", code) == nil {
		t.Fatal("cross-channel code accepted")
	}
	if security.LinkBotAccount(security.WithTenantID(ctx, "link-b"), db, "tg:17", code) == nil {
		t.Fatal("another company's bot accepted code")
	}
	if err := security.LinkBotAccount(bot, db, "tg:17", code); err != nil {
		t.Fatal(err)
	}
	if security.LinkBotAccount(bot, db, "tg:18", code) == nil {
		t.Fatal("one-use code reused")
	}
	resolved, err := security.BotContext(bot, db, "tg:17")
	if err != nil || security.RequesterID(resolved) != p.ID || security.TenantID(resolved) != "link-a" {
		t.Fatal("linked identity not resolved")
	}
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	created, err := h.IngestMessage(bot, models.IngestRequest{Channel: "bot", Sender: "tg:17", Message: "Synthetic printer issue"})
	if err != nil {
		t.Fatal(err)
	}
	var tenant, requester, sourceBot string
	if db.QueryRow(ctx, `SELECT tenant_id,requester_id,bot_tenant_id FROM tickets WHERE id=$1`, created.TicketID).Scan(&tenant, &requester, &sourceBot) != nil || tenant != "link-a" || requester != p.ID || sourceBot != "default" {
		t.Fatal("bot ticket not attached to website account or delivery source lost")
	}
	selectedBot := ""
	h.WithTenantDispatcher(channels.NewTenantDispatcher(func(ctx context.Context, tenant string) (channels.Config, error) {
		selectedBot = tenant
		return channels.Config{}, errors.New("test delivery stopped before network")
	}))
	_ = h.Deliver(ctx, delivery.Message{Kind: "channel", TenantID: "link-a", Channel: "bot", Recipient: "tg:17", TicketID: created.TicketID, Body: "Synthetic reply"})
	if selectedBot != "default" {
		t.Fatal("operator response selected another company's bot")
	}
	var webID, foreignID int
	if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,user_email,channel,raw_text,status,rag_answer) VALUES('link-a',$1,$2,'web','Website question','awaiting_confirmation','Approved instruction') RETURNING id`, p.ID, p.Email).Scan(&webID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,channel,raw_text) VALUES('link-a','link-other','web','Private other question') RETURNING id`).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.BotTicket(bot, "tg:17", webID); err != nil {
		t.Fatal("linked Telegram cannot read own website ticket")
	}
	if _, err := h.BotTicket(bot, "tg:17", foreignID); err == nil {
		t.Fatal("linked Telegram read another employee's ticket")
	}
	if _, err := h.BotTicket(bot, "tg:18", webID); err == nil {
		t.Fatal("unlinked Telegram read website ticket")
	}
	if _, err := h.BotFeedback(bot, "tg:17", webID, true); err != nil {
		t.Fatal("linked feedback rejected")
	}
	r := httptest.NewRequest("GET", "/api/tickets", nil).WithContext(security.WithPrincipal(ctx, p))
	w := httptest.NewRecorder()
	h.List(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"id":%d`, created.TicketID)) || strings.Contains(w.Body.String(), "Private other question") {
		t.Fatal("website history omitted linked ticket or crossed ownership")
	}
	if _, err := db.Exec(ctx, `UPDATE workspace_members SET auth_version=auth_version+1 WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := security.BotContext(bot, db, "tg:17"); err == nil {
		t.Fatal("reset credentials did not invalidate bot link")
	}
	if change("DELETE", "telegram", p).Code != 200 {
		t.Fatal("unlink failed")
	}
	if _, err := h.BotTicket(bot, "tg:17", webID); err == nil {
		t.Fatal("unlinked bot retained website access")
	}
	maxCode := issue("max")
	if security.LinkBotAccount(bot, db, "max_chat:17", maxCode) == nil {
		t.Fatal("group identity linked")
	}
	if _, err := db.Exec(ctx, `UPDATE bot_link_codes SET expires_at=NOW()-INTERVAL '1 second' WHERE member_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if security.LinkBotAccount(bot, db, "max:17", maxCode) == nil {
		t.Fatal("expired code accepted")
	}
	maxCode = issue("max")
	if err := security.LinkBotAccount(bot, db, "max:17", maxCode); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE workspace_members SET is_active=FALSE WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := security.BotContext(bot, db, "max:17"); err == nil {
		t.Fatal("disabled account retained bot access")
	}
	if change("POST", "telegram", security.Principal{ID: "link-admin", TenantID: "link-a", Role: "admin"}).Code != 403 {
		t.Fatal("admin linked privileged account to employee bot")
	}
}

func TestBotLinkCodeConsumedOnceUnderRace(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithTenantID(context.Background(), "default")
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('link-race','Race','fixture','race-hash'); INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('race-member','link-race','Race','race@example.org','employee','race-member-hash')`); err != nil {
		t.Fatal(err)
	}
	code := "link_" + strings.Repeat("x", 43)
	sum := sha256.Sum256([]byte(code))
	if _, err := db.Exec(ctx, `INSERT INTO bot_link_codes(token_hash,member_id,channel,auth_version,expires_at) VALUES($1,'race-member','telegram',0,NOW()+INTERVAL '10 minutes')`, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, sender := range []string{"tg:901", "tg:902"} {
		wg.Add(1)
		go func(s string) { defer wg.Done(); results <- security.LinkBotAccount(ctx, db, s, code) }(sender)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("one-use code succeeded %d times", success)
	}
}
