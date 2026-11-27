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
	"unicode/utf16"

	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

type employeeServiceFake struct {
	tickets        []models.Ticket
	ticket         models.Ticket
	err            error
	sender, tenant string
	feedbackCalls  int
	linkCalls      int
	resolved       bool
}

func (f *employeeServiceFake) LinkBot(ctx context.Context, sender, code string) error {
	f.linkCalls++
	f.sender = sender
	f.tenant = security.TenantID(ctx)
	return f.err
}

func TestBotLinkCodesNeverBecomeTicketsOrReachAI(t *testing.T) {
	code := "link_" + strings.Repeat("x", 43)
	for _, text := range []string{"/link " + code, "/start " + code, code, "мой код " + code, "/link"} {
		service := &employeeServiceFake{}
		update := decodeTelegramUpdate(t, fmt.Sprintf(`{"message":{"text":%q,"from":{"id":17},"chat":{"id":17,"type":"private"}}}`, text))
		err := handleTelegramEmployee(security.WithTenantID(context.Background(), "default"), update, service, nil, func(context.Context, models.IngestRequest) (IngestResult, error) {
			t.Fatal("link code forwarded to ingest/AI")
			return IngestResult{}, nil
		}, func(_ context.Context, method string, body any) error {
			encoded, _ := json.Marshal(body)
			if strings.Contains(string(encoded), code) {
				t.Fatal("link secret echoed in reply")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if (text == code || text == "/link "+code || text == "/start "+code) && (service.linkCalls != 1 || service.sender != "tg:17") {
			t.Fatal("verified private sender not linked")
		}
	}
	service := &employeeServiceFake{}
	if _, handled := botLinkCommand(context.Background(), "max_chat:12", "/link "+code, service); !handled || service.linkCalls != 0 {
		t.Fatal("MAX group consumed code")
	}
	if _, handled := botLinkCommand(context.Background(), "max:12", "/link "+code, service); !handled || service.linkCalls != 1 || service.sender != "max:12" {
		t.Fatal("MAX private link not processed")
	}
}

func (f *employeeServiceFake) BotTickets(ctx context.Context, sender string, before int) ([]models.Ticket, error) {
	f.sender = sender
	f.tenant = security.TenantID(ctx)
	return f.tickets, f.err
}
func (f *employeeServiceFake) BotTicket(ctx context.Context, sender string, id int) (models.Ticket, error) {
	f.sender = sender
	f.tenant = security.TenantID(ctx)
	return f.ticket, f.err
}
func (f *employeeServiceFake) BotFeedback(ctx context.Context, sender string, id int, resolved bool) (models.Ticket, error) {
	f.sender = sender
	f.tenant = security.TenantID(ctx)
	f.feedbackCalls++
	f.resolved = resolved
	return f.ticket, f.err
}

func decodeTelegramUpdate(t *testing.T, raw string) telegramUpdate {
	t.Helper()
	var update telegramUpdate
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	return update
}

func TestTelegramEmployeeMenuCommandsDoNotCreateTickets(t *testing.T) {
	ctx := security.WithTenantID(context.Background(), "company-a")
	for _, command := range []string{"/start", "/start@flowops_bot", "/menu", "/help", "/cancel", "/admin", "/ticket 42", "/ticket invalid"} {
		t.Run(command, func(t *testing.T) {
			update := decodeTelegramUpdate(t, fmt.Sprintf(`{"message":{"text":%q,"from":{"id":17},"chat":{"id":17,"type":"private"}}}`, command))
			service := &employeeServiceFake{ticket: models.Ticket{ID: 42, Status: "new"}}
			var body map[string]any
			err := handleTelegramEmployee(ctx, update, service, nil, func(context.Context, models.IngestRequest) (IngestResult, error) {
				t.Fatal("command ingested as a ticket")
				return IngestResult{}, nil
			}, func(_ context.Context, method string, value any) error {
				if method != "sendMessage" {
					t.Fatalf("unexpected method %s", method)
				}
				body = value.(map[string]any)
				return nil
			})
			if err != nil || body == nil {
				t.Fatalf("menu not sent: %v", err)
			}
			encoded, _ := json.Marshal(body)
			for _, forbidden := range []string{"emp:admin", "emp:operator", "emp:analytics", "emp:delete"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatal("privileged action in employee menu")
				}
			}
			if command == "/ticket 42" && (service.sender != "tg:17" || service.tenant != "company-a") {
				t.Fatal("requester identity lost")
			}
		})
	}
}

func TestTelegramCallbacksUseVerifiedPrivateSenderAndAcknowledgeFirst(t *testing.T) {
	ctx := security.WithTenantID(context.Background(), "company-b")
	for _, entry := range []struct {
		chatType       string
		chatID, userID int
		allowed        bool
	}{{"private", 17, 17, true}, {"private", 17, 18, false}, {"group", -19, 17, false}, {"supergroup", -20, 17, false}, {"", 0, 17, false}} {
		update := decodeTelegramUpdate(t, fmt.Sprintf(`{"callback_query":{"id":"callback-fixture","from":{"id":%d},"message":{"chat":{"id":%d,"type":%q}},"data":"emp:yes:42"}}`, entry.userID, entry.chatID, entry.chatType))
		service := &employeeServiceFake{ticket: models.Ticket{ID: 42, Status: "auto_resolved"}}
		calls := []string{}
		err := handleTelegramEmployee(ctx, update, service, nil, nil, func(_ context.Context, method string, body any) error { calls = append(calls, method); return nil })
		if err != nil || len(calls) == 0 || calls[0] != "answerCallbackQuery" {
			t.Fatalf("callback not acknowledged: %v", err)
		}
		if entry.allowed {
			if service.feedbackCalls != 1 || !service.resolved || service.sender != "tg:17" || service.tenant != "company-b" {
				t.Fatal("verified callback not scoped")
			}
		} else if service.feedbackCalls != 0 || len(calls) != 1 {
			t.Fatal("group or foreign callback exposed employee data")
		}
	}
}

func TestTelegramTextCreatesConfirmedTicketAndActionButtons(t *testing.T) {
	update := decodeTelegramUpdate(t, `{"message":{"text":"Не работает VPN","from":{"id":17},"chat":{"id":17,"type":"private"}}}`)
	drafts := newTelegramDrafts()
	drafts.open(17)
	var sent map[string]any
	err := handleTelegramEmployee(context.Background(), update, nil, nil, func(_ context.Context, r models.IngestRequest) (IngestResult, error) {
		if r.Sender != "tg:17" || r.Channel != "bot" || !r.RequireConfirmation || r.Message != "Не работает VPN" {
			t.Fatal("incorrect ingest identity")
		}
		return IngestResult{TicketID: 42, Status: "awaiting_confirmation", AutoReplied: true, Department: "IT", Priority: "medium", ReplyText: "Проверьте подключение."}, nil
	}, func(_ context.Context, method string, body any) error {
		if method == "sendMessage" {
			sent = body.(map[string]any)
		}
		return nil
	}, drafts)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sent)
	for _, action := range []string{"emp:yes:42", "emp:no:42", "emp:ticket:42", "emp:menu"} {
		if !strings.Contains(string(raw), action) {
			t.Fatalf("missing %s", action)
		}
	}
	drafts.open(17)
	// Failure sends a recovery message instead of claiming a ticket was created.
	err = handleTelegramEmployee(context.Background(), update, nil, nil, func(context.Context, models.IngestRequest) (IngestResult, error) {
		return IngestResult{}, errors.New("DB unavailable")
	}, func(_ context.Context, method string, body any) error {
		if method == "sendMessage" {
			sent = body.(map[string]any)
		}
		return nil
	}, drafts)
	if err != nil || !strings.Contains(sent["text"].(string), "Не удалось") {
		t.Fatal("missing retry guidance")
	}
}

func TestEmployeeMenuPaginationFeedbackAndEscaping(t *testing.T) {
	answer := strings.Repeat("Инструкция 😀\n", 1000)
	service := &employeeServiceFake{ticket: models.Ticket{ID: 42, Department: "IT", Status: "awaiting_confirmation", RAGAnswer: &answer, RawText: "<script>alert(1)</script>"}}
	ctx := context.Background()
	for i := 20; i > 11; i-- {
		service.tickets = append(service.tickets, models.Ticket{ID: i, Status: "new", RawText: "Проблема"})
	}
	list := employeeAction(ctx, "tg:17", "emp:list:0", service, nil)
	if len(list.Buttons) != 10 || list.Buttons[8][0].Data != "emp:list:13" {
		t.Fatal("incorrect keyset pagination")
	}
	screen := employeeAction(ctx, "tg:17", "emp:ticket:42:1", service, nil)
	if !strings.Contains(screen.Text, "Страница 2 из") {
		t.Fatal("long answer cannot be paged")
	}
	for _, row := range screen.Buttons {
		for _, button := range row {
			if len(button.Data) > 64 {
				t.Fatal("callback exceeds Telegram limit")
			}
		}
	}
	if units := len(utf16.Encode([]rune(screen.Text))); units > 4096 {
		t.Fatalf("Telegram text too large: %d", units)
	}
	body := telegramScreenBody(17, employeeTicketScreen(service.ticket, 0))
	if strings.Contains(body["text"].(string), "<script>") {
		t.Fatal("untrusted markup not escaped")
	}
	for _, status := range []string{"assigned_to_operator", "auto_resolved", "resolved", "closed"} {
		service.ticket.Status = status
		s := employeeTicketScreen(service.ticket, 0)
		for _, row := range s.Buttons {
			for _, b := range row {
				if strings.HasPrefix(b.Data, "emp:yes:") || strings.HasPrefix(b.Data, "emp:no:") {
					t.Fatal("stale feedback buttons offered")
				}
			}
		}
	}
	service.err = errors.New("not found")
	missing := employeeAction(ctx, "tg:17", "emp:ticket:999", service, nil)
	if strings.Contains(missing.Text, "<script>") || !strings.Contains(missing.Text, "недоступно") {
		t.Fatal("missing ticket leaked data")
	}
	for _, action := range []string{"admin:delete:42", "emp:ticket:-1", "emp:yes:42:0", "emp:ticket:42:-1", "emp:ticket:42:1001"} {
		before := service.feedbackCalls
		employeeAction(ctx, "tg:17", action, service, nil)
		if service.feedbackCalls != before {
			t.Fatal("malformed action mutated ticket")
		}
	}
	for _, page := range telegramTextPages(strings.Repeat("😀", 5000), 3500) {
		if len(utf16.Encode([]rune(page))) > 3500 {
			t.Fatal("emoji pagination exceeded limit")
		}
	}
	if _, present := telegramScreenBody(17, telegramScreen{Text: "Part one"})["reply_markup"]; present {
		t.Fatal("empty keyboard sent as null")
	}
}

type telegramRoundTripFunc func(*http.Request) (*http.Response, error)

func (f telegramRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramReplyUsesRealClientAndRejectsAPIFailure(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	requests := 0
	success := true
	http.DefaultTransport = telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Host != "api.telegram.org" || req.URL.Path != "/botfixture-token/sendMessage" || req.Method != "POST" {
			t.Fatal("unexpected Telegram request")
		}
		var payload struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
			Mode   string `json:"parse_mode"`
			Markup struct {
				Buttons [][]telegramButton `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.ChatID != 17 || payload.Mode != "HTML" || len(payload.Markup.Buttons) != 3 || payload.Markup.Buttons[0][0].Data != "emp:ticket:42" {
			t.Fatal("employee keyboard not sent through Telegram transport")
		}
		if strings.Contains(payload.Text, "<script>") {
			t.Fatal("unsafe Telegram markup")
		}
		body := `{"ok":true,"result":{"message_id":1}}`
		if !success {
			body = `{"ok":false,"description":"rejected"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	long := strings.Repeat("😀 instruction ", 1000) + "<script>"
	if err := sendTelegramMessage(context.Background(), "fixture-token", 17, long, 42); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatal("long notification flooded chat")
	}
	success = false
	if err := sendTelegramMessage(context.Background(), "fixture-token", 17, "Reply", 42); err == nil {
		t.Fatal("Telegram ok=false treated as delivered")
	}
}
