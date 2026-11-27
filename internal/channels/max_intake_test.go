package channels

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

func TestMaxWorkerClarificationFeedbackCancelAndMIDReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	update := func(user, chat int64, kind, mid, text string) maxUpdate {
		var u maxUpdate
		u.UpdateType = "message_created"
		u.Message.Sender.UserID = user
		u.Message.Recipient.ChatID = chat
		u.Message.Recipient.ChatType = kind
		u.Message.Body.MID = mid
		u.Message.Body.Text = text
		return u
	}
	location := update(77, 77, "dialog", "m2", "Корпус А, аудитория 313")
	final := update(77, 77, "dialog", "m3", "Проверили кабель")
	updates := []maxUpdate{
		update(77, 77, "dialog", "m1", "Проектор не показывает изображение. Через 10 минут занятие."),
		update(77, 900, "chat", "g1", "Сообщение группы"),
		location, location, final, final,
		update(77, 77, "dialog", "feedback-1", "/yes 42"), update(77, 77, "dialog", "feedback-2", "/no 42"),
		update(77, 77, "dialog", "m4", "Не работает проектор"), update(77, 77, "dialog", "m5", "/cancel"),
		update(77, 77, "dialog", "m6", "После отмены новая проблема"),
		update(77, 77, "dialog", "m7", "Не работает проектор"), update(77, 77, "dialog", "m8", "/skip"),
		update(0, 1001, "", "unknown-1", "Не работает проектор"), update(0, 1002, "", "unknown-2", "Не работает проектор"),
	}
	var mu sync.Mutex
	var messages []string
	polls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "synthetic-max-token" {
			t.Error("missing token header")
		}
		switch r.URL.Path {
		case "/updates":
			polls++
			if polls > 1 {
				cancel()
				_ = json.NewEncoder(w).Encode(map[string]any{"updates": []maxUpdate{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"updates": updates, "marker": 1})
		case "/messages":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			mu.Lock()
			messages = append(messages, body["text"])
			mu.Unlock()
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected MAX mock endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	maxClients.Store(false, server.Client())
	t.Cleanup(func() { maxClients.Delete(false) })
	var saved []models.IngestRequest
	ingest := func(ctx context.Context, r models.IngestRequest) (IngestResult, error) {
		if security.TenantID(ctx) != "university-pilot" {
			t.Error("worker lost tenant")
		}
		if r.Intake != nil {
			copyDetails := *r.Intake
			r.Intake = &copyDetails
		}
		saved = append(saved, r)
		return IngestResult{TicketID: 42, AutoReplied: true, Status: "awaiting_confirmation", ReplyText: "Проверьте инструкцию."}, nil
	}
	service := &employeeServiceFake{}
	runMax(ctx, Config{TenantID: "university-pilot", MaxToken: "synthetic-max-token", MaxAPIURL: server.URL}, ingest, slog.New(slog.NewTextHandler(io.Discard, nil)), service)
	if len(saved) != 6 {
		t.Fatalf("replay/cancel created an unexpected request: %+v", saved)
	}
	var clarified, skipped int
	for _, r := range saved {
		if r.Intake == nil {
			continue
		}
		if r.Intake.Skip {
			skipped++
			continue
		}
		clarified++
		if r.Sender != "max:77" || r.Intake.Location != "Корпус А, аудитория 313" || r.Intake.Tried != "Проверили кабель" || r.IdempotencyKey != "max-message:m1" {
			t.Fatalf("context/key lost: %+v", r)
		}
	}
	if clarified != 1 || skipped != 1 {
		t.Fatalf("clarification lost: normal=%d skip=%d", clarified, skipped)
	}
	if service.feedbackCalls != 2 || service.resolved || service.sender != "max:77" || service.tenant != "university-pilot" {
		t.Fatal("feedback lost requester/tenant scoping")
	}
	mu.Lock()
	joined := strings.Join(messages, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "/yes 42") || !strings.Contains(joined, "Обращение отменено") || !strings.Contains(joined, "Где нужна помощь") {
		t.Fatal("worker failed to present clarification/feedback/cancel")
	}
}
