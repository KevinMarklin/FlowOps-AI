package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"flowops-ai/internal/models"
)

func TestTelegramConversationReusesScreenAndRemovesOnlySavedInput(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	type call struct {
		method string
		body   map[string]any
	}
	var calls []call
	http.DefaultTransport = telegramRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		method := strings.TrimPrefix(r.URL.Path, "/botfixture/")
		calls = append(calls, call{method, body})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":81}}`)), Header: make(http.Header)}, nil
	})
	ui := newTelegramUI("fixture")
	ctx := context.Background()
	drafts := newTelegramDrafts()
	start := decodeTelegramUpdate(t, `{"message":{"message_id":1,"text":"/start","from":{"id":17},"chat":{"id":17,"type":"private"}}}`)
	if err := handleTelegramEmployee(ctx, start, nil, nil, nil, ui.command, drafts); err != nil {
		t.Fatal(err)
	}
	next := decodeTelegramUpdate(t, `{"callback_query":{"id":"cb","from":{"id":17},"message":{"message_id":81,"chat":{"id":17,"type":"private"}},"data":"emp:new"}}`)
	if err := handleTelegramEmployee(ctx, next, nil, nil, nil, ui.command, drafts); err != nil {
		t.Fatal(err)
	}
	submit := decodeTelegramUpdate(t, `{"message":{"message_id":82,"text":"VPN broken","from":{"id":17},"chat":{"id":17,"type":"private"}}}`)
	ingest := func(context.Context, models.IngestRequest) (IngestResult, error) {
		return IngestResult{TicketID: 42, Status: "assigned_to_operator", Department: "IT", Priority: "medium"}, nil
	}
	if err := handleTelegramEmployee(ctx, submit, nil, nil, ingest, ui.command, drafts); err != nil {
		t.Fatal(err)
	}
	sends, edits, deletes := 0, 0, 0
	for _, c := range calls {
		switch c.method {
		case "sendMessage":
			sends++
		case "editMessageText":
			edits++
			if c.body["message_id"] != float64(81) {
				t.Fatal("wrong working screen")
			}
		case "deleteMessage":
			deletes++
			if c.body["message_id"] != float64(82) {
				t.Fatal("deleted unrelated message")
			}
		}
	}
	if sends != 1 || edits != 2 || deletes != 1 {
		t.Fatalf("chat clutter: sends=%d edits=%d deletes=%d", sends, edits, deletes)
	}
	// Failed submission keeps the user's draft in the chat.
	calls = nil
	drafts.open(17)
	if err := handleTelegramEmployee(ctx, submit, nil, nil, func(context.Context, models.IngestRequest) (IngestResult, error) {
		return IngestResult{}, errors.New("offline")
	}, ui.command, drafts); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c.method == "deleteMessage" {
			t.Fatal("failed draft deleted")
		}
	}
}

func TestTelegramReadChecksOwnershipAndDeletesOnlyClickedNotification(t *testing.T) {
	reply := "Operator answer"
	for _, tc := range []struct {
		name      string
		user      int
		chatType  string
		lookupErr error
		allowed   bool
	}{
		{"owner", 17, "private", nil, true}, {"foreign sender", 18, "private", nil, false}, {"group", 17, "group", nil, false}, {"foreign ticket", 17, "private", errors.New("not found"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &employeeServiceFake{ticket: models.Ticket{ID: 42, OperatorReply: &reply}, err: tc.lookupErr}
			update := decodeTelegramUpdate(t, fmt.Sprintf(`{"callback_query":{"id":"cb","from":{"id":%d},"message":{"message_id":99,"chat":{"id":17,"type":%q}},"data":"emp:read:42"}}`, tc.user, tc.chatType))
			deleted := false
			err := handleTelegramEmployee(context.Background(), update, service, nil, nil, func(_ context.Context, method string, value any) error {
				if method == "deleteMessage" {
					deleted = true
					body := value.(map[string]any)
					if body["message_id"] != int64(99) || body["chat_id"] != int64(17) {
						t.Fatal("wrong delete target")
					}
				}
				if method != "deleteMessage" && method != "answerCallbackQuery" {
					t.Fatal("read created chat clutter")
				}
				return nil
			})
			if err != nil || deleted != tc.allowed {
				t.Fatalf("unexpected read result %v %v", deleted, err)
			}
			if service.feedbackCalls != 0 || service.ticket.OperatorReply == nil || *service.ticket.OperatorReply != reply {
				t.Fatal("read modified ticket history")
			}
		})
	}
}

func TestTelegramStaleScreenRecoveryAndAPIFailures(t *testing.T) {
	for _, description := range []string{"Bad Request: message is not modified", "Bad Request: message to edit not found", "Bad Request: message can't be edited", "Too Many Requests"} {
		t.Run(description, func(t *testing.T) {
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			calls := 0
			http.DefaultTransport = telegramRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				code := 400
				payload := fmt.Sprintf(`{"ok":false,"description":%q}`, description)
				if description == "Too Many Requests" {
					code = 429
				}
				if calls == 2 {
					code = 200
					payload = `{"ok":true,"result":{"message_id":120}}`
					if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
						t.Fatal("stale edit did not create replacement")
					}
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
			})
			ui := newTelegramUI("fixture")
			err := ui.command(context.Background(), "editMessageText", map[string]any{"chat_id": int64(17), "message_id": int64(81), "text": "screen"})
			if description == "Too Many Requests" {
				if err == nil || calls != 1 {
					t.Fatal("rate limit hidden or retried")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(description, "not modified") {
				if calls != 1 || ui.chats[17].id != 81 {
					t.Fatal("duplicate screen on repeated button")
				}
			} else if calls != 2 || ui.chats[17].id != 120 {
				t.Fatal("replacement not remembered")
			}
		})
	}
}

func TestTelegramReadOldNotificationClearsTextAndKeyboard(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	calls := 0
	http.DefaultTransport = telegramRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["chat_id"] != float64(17) || body["message_id"] != float64(99) {
			t.Fatal("cleanup changed target")
		}
		code, payload := 400, `{"ok":false,"description":"Bad Request: message can't be deleted"}`
		if calls == 2 {
			if !strings.HasSuffix(r.URL.Path, "/editMessageText") || body["text"] != "✅ Прочитано" {
				t.Fatal("old reply was not cleared")
			}
			markup := body["reply_markup"].(map[string]any)
			if len(markup["inline_keyboard"].([]any)) != 0 {
				t.Fatal("read button still present")
			}
			code, payload = 200, `{"ok":true,"result":{"message_id":99}}`
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
	})
	ui := newTelegramUI("fixture")
	if err := ui.command(context.Background(), "deleteMessage", map[string]any{"chat_id": int64(17), "message_id": int64(99)}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("old notification not handled")
	}
}
