package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"flowops-ai/internal/models"
)

func TestTelegramTicketsRequireNewButtonEachTime(t *testing.T) {
	drafts := newTelegramDrafts()
	ctx := context.Background()
	created := 0
	failSave, failScreen := false, false
	ingest := func(context.Context, models.IngestRequest) (IngestResult, error) {
		if failSave {
			return IngestResult{}, errors.New("offline")
		}
		created++
		return IngestResult{TicketID: 42, Status: "assigned_to_operator"}, nil
	}
	var lastText string
	command := func(_ context.Context, method string, value any) error {
		if method == "sendMessage" || method == "editMessageText" {
			lastText = value.(map[string]any)["text"].(string)
			if failScreen {
				return errors.New("Telegram offline")
			}
		}
		return nil
	}
	send := func(text string, chat int64) error {
		update := decodeTelegramUpdate(t, fmt.Sprintf(`{"message":{"text":%q,"from":{"id":%d},"chat":{"id":%d,"type":"private"}}}`, text, chat, chat))
		return handleTelegramEmployee(ctx, update, nil, nil, ingest, command, drafts)
	}
	press := func(action string) error {
		update := decodeTelegramUpdate(t, fmt.Sprintf(`{"callback_query":{"id":"cb","from":{"id":17},"message":{"message_id":81,"chat":{"id":17,"type":"private"}},"data":%q}}`, action))
		return handleTelegramEmployee(ctx, update, nil, nil, ingest, command, drafts)
	}
	if err := send("Hello", 17); err != nil || created != 0 || !strings.Contains(lastText, "Новое обращение") {
		t.Fatal("unsolicited text created ticket or lacked guidance")
	}
	if err := press("emp:new"); err != nil {
		t.Fatal(err)
	}
	_ = send("Another user's text", 18)
	if created != 0 {
		t.Fatal("draft leaked between chats")
	}
	_ = send("VPN broken", 17)
	_ = send("Thanks", 17)
	if created != 1 || drafts.waiting(17) {
		t.Fatal("draft was not closed after success")
	}
	_ = press("emp:new")
	_ = press("emp:menu")
	_ = send("Cancelled", 17)
	if created != 1 {
		t.Fatal("cancelled draft accepted")
	}
	_ = press("emp:new")
	_ = send("/cancel", 17)
	_ = send("Cancelled", 17)
	if created != 1 {
		t.Fatal("cancel command failed")
	}
	failSave = true
	_ = press("emp:new")
	_ = send("Retry me", 17)
	if !drafts.waiting(17) {
		t.Fatal("failed save closed draft")
	}
	failSave = false
	failScreen = true
	_ = send("Retry me", 17)
	if created != 2 || drafts.waiting(17) {
		t.Fatal("saved ticket remained open after delivery failure")
	}
	failScreen = false
	_ = send("Retry me", 17)
	if created != 2 {
		t.Fatal("delivery failure caused duplicate ticket")
	}
	failScreen = true
	_ = press("emp:new")
	if drafts.waiting(17) {
		t.Fatal("failed prompt enabled input")
	}
}

func TestTelegramDraftExpiryAndWorkerIsolation(t *testing.T) {
	a, b := newTelegramDrafts(), newTelegramDrafts()
	a.open(17)
	if b.waiting(17) {
		t.Fatal("draft leaked between bot workers")
	}
	a.chats[17] = time.Now().Add(-time.Second)
	if a.waiting(17) {
		t.Fatal("expired draft accepted")
	}
}
