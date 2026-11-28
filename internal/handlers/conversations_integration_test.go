package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/channels"
	"flowops-ai/internal/delivery"
	"flowops-ai/internal/intake"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
)

func TestDialogueMessagesRatingsClosureAndIsolation(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithTenantID(context.Background(), "dialogue-a")
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	var id int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,user_email,raw_text,rag_answer,status) VALUES('dialogue-a','employee-a','employee@example.org','Initial question','Approved regulation','awaiting_confirmation') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	employee := dialogueActor{id: "employee-a"}
	operator := dialogueActor{id: "operator-a", operator: true}
	transcript, err := h.readDialogue(ctx, id, employee, 0)
	if err != nil || len(transcript.Messages) != 2 {
		t.Fatalf("initial transcript: %+v %v", transcript, err)
	}
	assistantID := transcript.Messages[1].ID
	if _, err = h.appendDialogue(ctx, id, dialogueActor{id: "employee-b"}, "Forged message", "fake", false); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("another employee appended a message", err)
	}
	if _, err = h.readDialogue(security.WithTenantID(ctx, "dialogue-b"), id, operator, 0); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-tenant read", err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.appendDialogue(ctx, id, employee, "Still not working", "same-request", false)
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = h.appendDialogue(ctx, id, employee, "Changed payload", "same-request", false); !errors.Is(err, errRequestConflict) {
		t.Fatal("retry accepted changed body", err)
	}
	transcript, err = h.readDialogue(ctx, id, employee, 0)
	if err != nil || len(transcript.Messages) != 3 {
		t.Fatal("duplicate or missing messages", len(transcript.Messages), err)
	}
	for i := 0; i < 2; i++ {
		if _, err = h.appendDialogue(ctx, id, operator, fmt.Sprintf("Operator answer %d", i), fmt.Sprintf("reply-%d", i), false); err != nil {
			t.Fatal(err)
		}
	}
	transcript, err = h.readDialogue(ctx, id, employee, 0)
	if err != nil || len(transcript.Messages) != 5 || transcript.Status != "awaiting_confirmation" {
		t.Fatal("operator reply closed or overwrote dialogue", transcript, err)
	}
	if err = h.rateDialogue(ctx, id, assistantID, employee, 2, "Too general"); err != nil {
		t.Fatal(err)
	}
	if err = h.rateDialogue(ctx, id, assistantID, employee, 4, "Now clearer"); err != nil {
		t.Fatal(err)
	}
	if err = h.rateDialogue(ctx, id, transcript.Messages[0].ID, employee, 5, ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("rated employee message", err)
	}
	if err = h.rateDialogue(ctx, id, assistantID, dialogueActor{id: "employee-b"}, 5, ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("another employee rated answer", err)
	}
	transcript, err = h.readDialogue(ctx, id, employee, 0)
	if err != nil || transcript.Messages[1].Rating != 4 {
		t.Fatal("rating not saved", err)
	}
	if _, err = h.decideDialogue(ctx, id, employee, false); err != nil {
		t.Fatal(err)
	}
	if _, err = h.appendDialogue(ctx, id, employee, "Additional detail", "detail-2", false); err != nil {
		t.Fatal("not-helpful stopped dialogue", err)
	}
	if _, err = h.decideDialogue(ctx, id, employee, true); err != nil {
		t.Fatal(err)
	}
	if _, err = h.appendDialogue(ctx, id, employee, "Too late", "after-close", false); !errors.Is(err, errDialogueClosed) {
		t.Fatal("closed dialogue accepted message", err)
	}
	if err = h.rateDialogue(ctx, id, assistantID, employee, 5, "Final feedback"); err != nil {
		t.Fatal("closed answer cannot be rated", err)
	}
	// HTTP denial uses the same ownership check and does not expose message text.
	request := httptest.NewRequest("GET", fmt.Sprintf("/api/tickets/%d/messages", id), nil).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "employee-b", TenantID: "dialogue-a", Role: "employee"}))
	request.SetPathValue("id", fmt.Sprint(id))
	response := httptest.NewRecorder()
	h.Messages(response, request)
	if response.Code != 404 || strings.Contains(response.Body.String(), "Approved regulation") {
		t.Fatal("HTTP transcript leaked")
	}
}

func TestClarificationReplayAfterRestartDoesNotBecomeDialogueMessage(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithTenantID(context.Background(), "receipt-tenant")
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	makeHandler := func() *TicketsHandler {
		return NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	}
	h := makeHandler()
	created, err := h.IngestMessage(ctx, models.IngestRequest{Channel: "bot", Sender: "max:20", Message: "Проектор не работает", Intake: &intake.Details{Skip: true}, IdempotencyKey: "max-message:first", BotMessageKeys: []string{"max-message:first", "max-message:final"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.BotActivate(ctx, "max:20", created.TicketID); err != nil {
		t.Fatal(err)
	}
	h = makeHandler()
	replay, err := h.BotContinue(ctx, "max:20", "/skip", "max-message:final")
	if err != nil || len(replay.Messages) != 1 || replay.ID != created.TicketID {
		t.Fatal("final clarification was appended after restart", err, replay)
	}
}

func TestLinkedBotDialogueSurvivesRestartAndPreservesEveryDelivery(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithTenantID(context.Background(), "company-dialogue")
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('company-dialogue','Company','test','dialogue-owner'); INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('linked-employee','company-dialogue','Employee','employee@example.org','employee','dialogue-member'); INSERT INTO member_bot_links(bot_tenant_id,sender,channel,member_id,auth_version) VALUES('shared-bot','tg:17','telegram','linked-employee',0),('shared-bot','max:17','max','linked-employee',0)`); err != nil {
		t.Fatal(err)
	}
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	makeHandler := func() *TicketsHandler {
		return NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	}
	h := makeHandler()
	var id int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,requester_id,user_email,channel,raw_text,status) VALUES('company-dialogue','linked-employee','employee@example.org','web','Website question','assigned_to_operator') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	botCtx := security.WithTenantID(context.Background(), "shared-bot")
	if err = h.BotActivate(botCtx, "tg:17", id); err != nil {
		t.Fatal(err)
	}
	h = makeHandler()
	transcript, err := h.BotContinue(botCtx, "tg:17", "Telegram follow-up", "tg-message:9")
	if err != nil || len(transcript.Messages) != 2 {
		t.Fatal("restart lost selected dialogue", err)
	}
	if err = h.BotActivate(botCtx, "max:17", id); err != nil {
		t.Fatal(err)
	}
	if _, err = h.BotContinue(botCtx, "max:17", "MAX follow-up", "max-message:8"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = h.appendDialogue(ctx, id, dialogueActor{id: "operator", operator: true}, fmt.Sprintf("Answer %d", i), fmt.Sprintf("answer-%d", i), false); err != nil {
			t.Fatal(err)
		}
	}
	var pending int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM ticket_deliveries WHERE ticket_id=$1 AND state='pending' AND conversation_message_id IS NOT NULL`, id).Scan(&pending); err != nil || pending != 4 {
		t.Fatalf("pending messages %d want 4: %v", pending, err)
	}
	delivered := 0
	q := delivery.New(db, func(context.Context, delivery.Message) error { delivered++; return nil })
	for i := 0; i < 4; i++ {
		worked, err := q.ProcessOnce(ctx)
		if err != nil || !worked {
			t.Fatal("delivery failed", err)
		}
	}
	if delivered != 4 {
		t.Fatal("lost delivery")
	}
	if _, err = h.appendDialogue(ctx, id, dialogueActor{id: "operator", operator: true}, "One final message", "answer-3", false); err != nil {
		t.Fatal(err)
	}
	if _, err = h.BotDecide(botCtx, "max:17", id, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = q.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	transcript, err = h.BotTicket(botCtx, "tg:17", id)
	if err != nil || transcript.Status != "closed" || len(transcript.Messages) != 7 {
		t.Fatal("delivery closed dialogue or lost history", err, transcript.Status)
	}
	if _, err = h.BotTicket(botCtx, "tg:18", id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("unlinked sender read another account", err)
	}
	// Removing a link cannot turn a remembered ticket into an anonymous ticket.
	if _, err = db.Exec(ctx, `DELETE FROM member_bot_links WHERE sender='tg:17'`); err != nil {
		t.Fatal(err)
	}
	if _, err = h.BotContinue(botCtx, "tg:17", "Forged continuation", "tg-message:10"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("unlinked account retained access", err)
	}
	if err = h.BotActivate(botCtx, "max:17", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = h.BotContinue(botCtx, "max:17", "New question", "max-message:11"); !errors.Is(err, channels.ErrNoActiveDialogue) {
		t.Fatal("explicit new ticket continued old dialogue", err)
	}
}
