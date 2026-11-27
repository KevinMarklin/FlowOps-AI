package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flowops-ai/internal/models"
)

type dialogueServiceFake struct {
	employeeServiceFake
	continued int
	active    int
	score     int
	message   int64
	key       string
}

func (f *dialogueServiceFake) BotActivate(ctx context.Context, sender string, id int) error {
	f.sender = sender
	f.active = id
	return f.err
}
func (f *dialogueServiceFake) BotContinue(ctx context.Context, sender, text, key string) (models.Ticket, error) {
	f.sender = sender
	f.key = key
	f.continued++
	return f.ticket, f.err
}
func (f *dialogueServiceFake) BotRate(ctx context.Context, sender string, id int, message int64, score int) error {
	f.sender = sender
	f.active = id
	f.message = message
	f.score = score
	return f.err
}
func (f *dialogueServiceFake) BotDecide(ctx context.Context, sender string, id int, resolved bool) (models.Ticket, error) {
	f.sender = sender
	f.resolved = resolved
	return f.ticket, f.err
}

func TestTelegramPlainMessageContinuesTicketWithoutButtonOrNewIntake(t *testing.T) {
	service := &dialogueServiceFake{employeeServiceFake: employeeServiceFake{ticket: models.Ticket{ID: 42, Status: "assigned_to_operator", Messages: []models.TicketMessage{{ID: 1, Role: "employee", Body: "Details"}}}}}
	update := decodeTelegramUpdate(t, `{"message":{"message_id":8,"text":"Additional detail","from":{"id":17},"chat":{"id":17,"type":"private"}}}`)
	sends := 0
	err := handleTelegramEmployee(context.Background(), update, service, nil, func(context.Context, models.IngestRequest) (IngestResult, error) {
		t.Fatal("continuation created another ticket")
		return IngestResult{}, nil
	}, func(ctx context.Context, method string, value any) error {
		if method == "deleteMessage" || method == "editMessageText" {
			t.Fatal("conversation history was removed")
		}
		if method == "sendMessage" {
			sends++
			body := value.(map[string]any)
			if body["keep_history"] != true {
				t.Fatal("reply will overwrite history")
			}
		}
		return nil
	})
	if err != nil || sends != 1 || service.continued != 1 || service.sender != "tg:17" || service.key != "tg-message:8" {
		t.Fatal("continuation failed", err, service)
	}
}

func TestTelegramRatingUsesVerifiedIdentityAndSelectedAnswer(t *testing.T) {
	service := &dialogueServiceFake{employeeServiceFake: employeeServiceFake{ticket: models.Ticket{ID: 42, Status: "awaiting_confirmation", Messages: []models.TicketMessage{{ID: 51, Role: "assistant", Body: "Instruction"}}}}}
	for _, sender := range []int{18, 17} {
		raw := `{"callback_query":{"id":"callback","from":{"id":SENDER},"message":{"message_id":3,"chat":{"id":17,"type":"private"}},"data":"emp:rate:42:51:4"}}`
		if sender == 18 {
			raw = strings.Replace(raw, "SENDER", "18", 1)
		} else {
			raw = strings.Replace(raw, "SENDER", "17", 1)
		}
		update := decodeTelegramUpdate(t, raw)
		calls := []string{}
		if err := handleTelegramEmployee(context.Background(), update, service, nil, nil, func(_ context.Context, method string, _ any) error { calls = append(calls, method); return nil }); err != nil {
			t.Fatal(err)
		}
		if len(calls) == 0 || calls[0] != "answerCallbackQuery" {
			t.Fatal("callback not acknowledged first")
		}
		if sender == 18 && service.score != 0 {
			t.Fatal("forwarded callback rated another user's answer")
		}
	}
	if service.sender != "tg:17" || service.score != 4 || service.message != 51 {
		t.Fatal("wrong rating target")
	}
}

func TestTelegramConfiguresNativeMenuBesideInput(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	methods := []string{}
	http.DefaultTransport = telegramRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		methods = append(methods, strings.TrimPrefix(r.URL.Path, "/botfixture/"))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(r.URL.Path, "setChatMenuButton") && body["menu_button"].(map[string]any)["type"] != "commands" {
			t.Fatal("not a native command menu")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`)), Header: make(http.Header)}, nil
	})
	if err := configureTelegramMenu(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "setMyCommands,setChatMenuButton" {
		t.Fatal("menu registration missing", methods)
	}
}

func TestMAXCallbackRatesAnswerAndUsesInlineKeyboard(t *testing.T) {
	service := &dialogueServiceFake{employeeServiceFake: employeeServiceFake{ticket: models.Ticket{ID: 42, Status: "awaiting_confirmation", Messages: []models.TicketMessage{{ID: 51, Role: "assistant", Body: "Instruction"}}}}}
	methods := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		if r.URL.Path == "/messages" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if _, ok := body["attachments"]; !ok {
				t.Error("rating screen has no keyboard")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true}`)
	}))
	defer server.Close()
	var update maxUpdate
	if err := json.Unmarshal([]byte(`{"update_type":"message_callback","callback":{"callback_id":"test","payload":"emp:rate:42:51:5","user":{"user_id":17}},"message":{"recipient":{"chat_type":"dialog","chat_id":17}}}`), &update); err != nil {
		t.Fatal(err)
	}
	if err := handleMaxCallback(context.Background(), Config{MaxToken: "fixture", MaxAPIURL: server.URL, MaxInsecureTLS: true}, update, service); err != nil {
		t.Fatal(err)
	}
	if service.sender != "max:17" || service.score != 5 || service.message != 51 || strings.Join(methods, ",") != "/answers,/messages" {
		t.Fatal("MAX rating failed", service, methods)
	}
	update.Message.Recipient.ChatType = "chat"
	service.score = 0
	if err := handleMaxCallback(context.Background(), Config{}, update, service); err != nil || service.score != 0 {
		t.Fatal("group callback changed a private ticket")
	}
}
