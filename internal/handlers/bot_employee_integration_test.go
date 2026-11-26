package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"

	"flowops-ai/internal/channels"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
)

func TestBotEmployeeHistoryOwnershipAndAtomicFeedback(t *testing.T) {
	db := replyTestDB(t)
	h := &TicketsHandler{db: db}
	ctx := security.WithTenantID(context.Background(), "bot-company-a")
	otherCompany := security.WithTenantID(context.Background(), "bot-company-b")
	insert := func(tenant, sender, channel, status string, rag, reply any) int {
		t.Helper()
		var id int
		if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,user_email,channel,raw_text,department,priority,status,rag_answer,operator_reply) VALUES($1,$2,$3,'Synthetic problem','IT','medium',$4,$5,$6) RETURNING id`, tenant, sender, channel, status, rag, reply).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ids := []int{}
	for i := 0; i < 12; i++ {
		ids = append(ids, insert("bot-company-a", "tg:17", "bot", "awaiting_confirmation", "Open the VPN client.", nil))
	}
	foreign := insert("bot-company-a", "tg:18", "bot", "awaiting_confirmation", "Private answer", nil)
	foreignTenant := insert("bot-company-b", "tg:17", "bot", "awaiting_confirmation", "Other company", nil)
	web := insert("bot-company-a", "tg:17", "web", "awaiting_confirmation", "Web answer", nil)
	max := insert("bot-company-a", "max:17", "bot", "awaiting_confirmation", "MAX answer", nil)
	page, err := h.BotTickets(ctx, "tg:17", 0)
	if err != nil || len(page) != 9 || page[0].ID != ids[11] {
		t.Fatalf("incorrect history: len=%d err=%v", len(page), err)
	}
	older, err := h.BotTickets(ctx, "tg:17", page[7].ID)
	if err != nil || len(older) != 4 || older[0].ID != ids[3] {
		t.Fatal("pagination loses or duplicates tickets")
	}
	for _, id := range []int{foreign, foreignTenant, web, max} {
		if _, err := h.BotTicket(ctx, "tg:17", id); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("foreign ticket exposed")
		}
		if _, err := h.BotFeedback(ctx, "tg:17", id, true); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("foreign ticket mutated")
		}
	}
	if _, err := h.BotTicket(otherCompany, "tg:17", ids[0]); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-tenant detail exposed")
	}
	for _, sender := range []string{"tg:-17", "tg:017", "tg:0", "tg:17 ", "max:17", "17", ""} {
		if _, err := h.BotTickets(ctx, sender, 0); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("invalid sender accepted")
		}
	}
	first, err := h.BotFeedback(ctx, "tg:17", ids[0], false)
	if err != nil || first.Status != "assigned_to_operator" {
		t.Fatalf("handoff failed: %v", err)
	}
	if _, err := h.BotFeedback(ctx, "tg:17", ids[0], false); err != nil {
		t.Fatal("repeated same callback must be idempotent")
	}
	if _, err := h.BotFeedback(ctx, "tg:17", ids[0], true); !errors.Is(err, channels.ErrBotFeedbackClosed) {
		t.Fatal("opposite stale callback overwrote handoff")
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE tenant_id='bot-company-a' AND ticket_id=$1 AND event_type='TICKET_FEEDBACK'`, ids[0]).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("duplicate feedback audit")
	}
	for _, entry := range []struct {
		status     string
		rag, reply any
	}{{"closed", "Answer", nil}, {"new", nil, nil}, {"awaiting_confirmation", nil, nil}, {"awaiting_confirmation", "Answer", "Operator reply"}} {
		id := insert("bot-company-a", "tg:17", "bot", entry.status, entry.rag, entry.reply)
		if _, err := h.BotFeedback(ctx, "tg:17", id, true); !errors.Is(err, channels.ErrBotFeedbackClosed) {
			t.Fatal("closed, missing-regulation or operator answer accepted feedback")
		}
	}
	// Two opposite buttons pressed concurrently must not both succeed.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, resolved := range []bool{false, true} {
		wg.Add(1)
		go func(value bool) { defer wg.Done(); _, err := h.BotFeedback(ctx, "tg:17", ids[1], value); errs <- err }(resolved)
	}
	wg.Wait()
	close(errs)
	successes, stale := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, channels.ErrBotFeedbackClosed) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("feedback race: succeeded=%d stale=%d", successes, stale)
	}
}
