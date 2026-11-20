package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"flowops-ai/internal/models"
)

const welcomeText = "Здравствуйте! Я ИИ-помощник службы поддержки FlowOps AI. Опишите вашу проблему — я сразу найду решение по регламенту или передам заявку профильному специалисту."

type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
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
	return (&http.Client{Timeout: 40 * time.Second}).Do(req)
}

func query(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}

func sendTelegramMessage(ctx context.Context, token string, chatID int64, text string) error {
	resp, err := telegramRequest(ctx, token, "sendMessage", nil, map[string]any{"chat_id": chatID, "text": text})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Telegram sendMessage returned %s", resp.Status)
	}
	return nil
}

func runTelegram(ctx context.Context, token string, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) {
	var offset int64
	client := &http.Client{Timeout: 40 * time.Second}
	for ctx.Err() == nil {
		values := url.Values{"timeout": {"30"}}
		if offset > 0 {
			values.Set("offset", strconv.FormatInt(offset, 10))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/bot"+token+"/getUpdates?"+values.Encode(), nil)
		if err == nil {
			var resp *http.Response
			resp, err = client.Do(req)
			if err == nil {
				var payload struct {
					OK     bool             `json:"ok"`
					Result []telegramUpdate `json:"result"`
				}
				err = json.NewDecoder(resp.Body).Decode(&payload)
				resp.Body.Close()
				if err == nil && !payload.OK {
					err = fmt.Errorf("Telegram getUpdates returned ok=false")
				}
				if err == nil {
					for _, update := range payload.Result {
						if update.UpdateID >= offset {
							offset = update.UpdateID + 1
						}
						text := strings.TrimSpace(update.Message.Text)
						chatID := update.Message.Chat.ID
						if chatID == 0 || text == "" {
							continue
						}
						if text == "/start" || strings.HasPrefix(text, "/start@") || strings.HasPrefix(text, "/start ") {
							if err := sendTelegramMessage(ctx, token, chatID, welcomeText); err != nil {
								logger.Error("Telegram welcome failed", "error", err)
							}
							continue
						}
						actionResp, actionErr := telegramRequest(ctx, token, "sendChatAction", nil, map[string]any{"chat_id": chatID, "action": "typing"})
						if actionErr == nil {
							actionResp.Body.Close()
						}
						result, ingestErr := ingest(ctx, models.IngestRequest{Channel: "bot", Sender: fmt.Sprintf("tg:%d", chatID), Message: text})
						if ingestErr != nil {
							logger.Error("Telegram ingest failed", "error", ingestErr)
							continue
						}
						reply := result.ReplyText
						if !result.AutoReplied {
							reply = fmt.Sprintf("Ваше обращение передано специалисту поддержки. Номер заявки: #%d. Ответ оператора придёт сюда автоматически.", result.TicketID)
						}
						if err := sendTelegramMessage(ctx, token, chatID, reply); err != nil {
							logger.Error("Telegram reply failed", "error", err)
						}
					}
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			logger.Error("Telegram polling failed", "error", err)
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
