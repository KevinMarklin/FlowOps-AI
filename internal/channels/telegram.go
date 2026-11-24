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
	"flowops-ai/internal/settings"
)

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
	return (&http.Client{Timeout: 40 * time.Second, CheckRedirect: rejectChannelRedirect}).Do(req)
}

func query(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}

func sendTelegramMessage(ctx context.Context, token string, chatID int64, text string) error {
	resp, err := telegramRequest(ctx, token, "sendMessage", nil, map[string]any{"chat_id": chatID, "text": FormatTelegramMarkdown(text), "parse_mode": "HTML"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Telegram sendMessage returned %s", resp.Status)
	}
	return nil
}

func runTelegram(ctx context.Context, token string, builderConfig settings.Loader, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) {
	var offset int64
	client := &http.Client{Timeout: 40 * time.Second, CheckRedirect: rejectChannelRedirect}
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
				err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload)
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
							if err := sendTelegramMessage(ctx, token, chatID, StartReplyText(ctx, builderConfig)); err != nil {
								logger.Error("Telegram welcome failed", "error", "operation failed")
							}
							continue
						}
						actionResp, actionErr := telegramRequest(ctx, token, "sendChatAction", nil, map[string]any{"chat_id": chatID, "action": "typing"})
						if actionErr == nil {
							actionResp.Body.Close()
						}
						result, ingestErr := ingest(ctx, models.IngestRequest{Channel: "bot", Sender: fmt.Sprintf("tg:%d", chatID), Message: text})
						if ingestErr != nil {
							logger.Error("Telegram ingest failed", "error", "operation failed")
							continue
						}
						reply := result.ReplyText
						if err := sendTelegramMessage(ctx, token, chatID, reply); err != nil {
							LogOutgoing(logger, "bot", fmt.Sprintf("tg:%d", chatID), result.TicketID, "AI", err)
						} else {
							LogOutgoing(logger, "bot", fmt.Sprintf("tg:%d", chatID), result.TicketID, "AI", nil)
						}
					}
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			logger.Error("Telegram polling failed", "error", "operation failed")
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
