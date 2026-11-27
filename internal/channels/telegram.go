package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type telegramUpdate struct {
	UpdateID int64           `json:"update_id"`
	Message  telegramMessage `json:"message"`
	Callback *struct {
		ID   string `json:"id"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Message telegramMessage `json:"message"`
		Data    string          `json:"data"`
	} `json:"callback_query"`
}
type telegramMessage struct {
	Text string `json:"text"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
}

func telegramRequest(ctx context.Context, token, method string, values url.Values, body any) (*http.Response, error) {
	endpoint := "https://api.telegram.org/bot" + token + "/" + method
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+query(values), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return (&http.Client{Timeout: 40 * time.Second, CheckRedirect: rejectChannelRedirect}).Do(req)
}
func query(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}
func sendTelegramMessage(ctx context.Context, token string, chatID int64, text string, ticketIDs ...int) error {
	id := 0
	if len(ticketIDs) > 0 {
		id = ticketIDs[0]
	}
	return sendTelegramScreen(ctx, token, chatID, employeeReplyScreen(id, text))
}
func telegramCommand(ctx context.Context, token, method string, body any) error {
	resp, err := telegramRequest(ctx, token, method, nil, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("Telegram response is invalid")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !result.OK {
		return fmt.Errorf("Telegram operation failed: HTTP %d", resp.StatusCode)
	}
	return nil
}
func telegramScreenBody(chatID int64, screen telegramScreen) map[string]any {
	body := map[string]any{"chat_id": chatID, "text": FormatTelegramMarkdown(screen.Text), "parse_mode": "HTML"}
	if len(screen.Buttons) > 0 {
		body["reply_markup"] = map[string]any{"inline_keyboard": screen.Buttons}
	}
	return body
}
func sendTelegramScreen(ctx context.Context, token string, chatID int64, screen telegramScreen) error {
	return telegramCommand(ctx, token, "sendMessage", telegramScreenBody(chatID, screen))
}
func runTelegram(ctx context.Context, tenantID, token string, builderConfig settings.Loader, statusUpdate func(string, string, string), ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}, services ...EmployeeTickets) {
	ctx = security.WithTenantID(ctx, tenantID)
	var employeeTickets EmployeeTickets
	if len(services) > 0 {
		employeeTickets = services[0]
	}
	var offset int64
	client := &http.Client{Timeout: 40 * time.Second, CheckRedirect: rejectChannelRedirect}
	for ctx.Err() == nil {
		values := url.Values{"timeout": {"30"}, "allowed_updates": {`["message","callback_query"]`}}
		if offset > 0 {
			values.Set("offset", strconv.FormatInt(offset, 10))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/bot"+token+"/getUpdates?"+values.Encode(), nil)
		if statusUpdate != nil {
			statusUpdate("telegram", "starting", "")
		}
		if err == nil {
			var resp *http.Response
			resp, err = client.Do(req)
			if err == nil {
				var payload struct {
					OK          bool             `json:"ok"`
					Result      []telegramUpdate `json:"result"`
					ErrorCode   int              `json:"error_code"`
					Description string           `json:"description"`
				}
				err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload)
				resp.Body.Close()
				if err == nil && !payload.OK {
					if payload.ErrorCode == http.StatusConflict {
						err = fmt.Errorf("Telegram 409 Conflict: %s", payload.Description)
					} else {
						err = fmt.Errorf("Telegram getUpdates returned ok=false: %s", payload.Description)
					}
				}
				if err != nil {
					if resp.StatusCode == http.StatusConflict {
						err = fmt.Errorf("Telegram 409 Conflict: another polling worker or webhook is active")
					}
					if strings.Contains(strings.ToLower(err.Error()), "conflict") {
						logger.Error("Telegram polling conflict", "tenant_id", tenantID, "channel", "telegram", "error", err.Error())
					}
				}
				if err == nil {
					if statusUpdate != nil {
						statusUpdate("telegram", "connected", "")
					}
					for _, update := range payload.Result {
						if update.UpdateID >= offset {
							offset = update.UpdateID + 1
						}
						var ticketID int
						loggedIngest := func(ctx context.Context, request models.IngestRequest) (IngestResult, error) {
							result, err := ingest(ctx, request)
							if err == nil {
								ticketID = result.TicketID
							}
							return result, err
						}
						err := handleTelegramEmployee(ctx, update, employeeTickets, builderConfig, loggedIngest, func(ctx context.Context, method string, body any) error {
							return telegramCommand(ctx, token, method, body)
						})
						if ticketID > 0 {
							LogOutgoing(logger, "bot", fmt.Sprintf("tg:%d", update.Message.Chat.ID), ticketID, "AI", err)
						}
						if err != nil {
							logger.Error("Telegram employee action failed", "error", "operation failed")
						}
					}
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			logger.Error("Telegram polling failed", "error", "operation failed")
			if statusUpdate != nil {
				statusUpdate("telegram", "error", err.Error())
			}
			waitContext(ctx, 3*time.Second)
		}
	}
}
func waitContext(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
