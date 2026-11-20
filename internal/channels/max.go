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

type maxUpdate struct {
	UpdateType string `json:"update_type"`
	Marker     int64  `json:"marker"`
	User       struct {
		UserID int64 `json:"user_id"`
	} `json:"user"`
	ChatID  int64 `json:"chat_id"`
	Message struct {
		Body struct {
			Text string `json:"text"`
		} `json:"body"`
		Sender struct {
			UserID int64 `json:"user_id"`
			IsBot  bool  `json:"is_bot"`
		} `json:"sender"`
		Recipient struct {
			ChatID   int64  `json:"chat_id"`
			ChatType string `json:"chat_type"`
		} `json:"recipient"`
	} `json:"message"`
}

func maxRequest(ctx context.Context, cfg Config, method, endpoint string, query url.Values, body any) (*http.Response, error) {
	target := cfg.maxURL() + endpoint
	if len(query) != 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", cfg.MaxToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return cfg.maxHTTPClient().Do(req)
}

func sendMaxMessage(ctx context.Context, cfg Config, targetType, targetID, text string) error {
	query := url.Values{targetType: {targetID}}
	resp, err := maxRequest(ctx, cfg, http.MethodPost, "/messages", query, map[string]string{"text": text})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MAX send message returned %s", resp.Status)
	}
	return nil
}

func runMax(ctx context.Context, cfg Config, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) {
	var marker int64
	for ctx.Err() == nil {
		query := url.Values{"timeout": {"30"}, "types": {"message_created,bot_started"}}
		if marker > 0 {
			query.Set("marker", strconv.FormatInt(marker, 10))
		}
		resp, err := maxRequest(ctx, cfg, http.MethodGet, "/updates", query, nil)
		if err == nil {
			var updates []maxUpdate
			var payload struct {
				Updates []maxUpdate `json:"updates"`
				Marker  int64       `json:"marker"`
			}
			err = json.NewDecoder(resp.Body).Decode(&payload)
			resp.Body.Close()
			updates = payload.Updates
			if payload.Marker > marker {
				marker = payload.Marker
			}
			if err == nil {
				for _, update := range updates {
					if update.Marker > marker {
						marker = update.Marker
					}
					if update.UpdateType == "bot_started" {
						userID := update.User.UserID
						if userID == 0 {
							userID = update.Message.Sender.UserID
						}
						chatID := update.ChatID
						if chatID == 0 {
							chatID = update.Message.Recipient.ChatID
						}
						target, targetID := "user_id", userID
						if targetID == 0 {
							target, targetID = "chat_id", chatID
						}
						if targetID != 0 {
							if sendErr := sendMaxMessage(ctx, cfg, target, strconv.FormatInt(targetID, 10), welcomeText); sendErr != nil {
								logger.Error("MAX welcome failed", "error", sendErr)
							}
						}
						continue
					}
					if update.UpdateType != "message_created" || update.Message.Sender.IsBot {
						continue
					}
					text := strings.TrimSpace(update.Message.Body.Text)
					if text == "" {
						continue
					}
					userID := update.Message.Sender.UserID
					chatID := update.Message.Recipient.ChatID
					if chatID == 0 {
						chatID = update.ChatID
					}
					sender, targetType, targetID := "", "", int64(0)
					isGroup := strings.EqualFold(update.Message.Recipient.ChatType, "chat") || strings.EqualFold(update.Message.Recipient.ChatType, "channel")
					if isGroup && chatID != 0 {
						sender, targetType, targetID = fmt.Sprintf("max_chat:%d", chatID), "chat_id", chatID
					} else if userID != 0 {
						sender, targetType, targetID = fmt.Sprintf("max:%d", userID), "user_id", userID
					} else if chatID != 0 {
						sender, targetType, targetID = fmt.Sprintf("max_chat:%d", chatID), "chat_id", chatID
					}
					if sender == "" {
						continue
					}
					result, ingestErr := ingest(ctx, models.IngestRequest{Channel: "bot", Sender: sender, Message: text})
					if ingestErr != nil {
						logger.Error("MAX ingest failed", "error", ingestErr)
						continue
					}
					reply := result.ReplyText
					if !result.AutoReplied {
						reply = fmt.Sprintf("Ваше обращение передано специалисту поддержки. Номер заявки: #%d. Ответ оператора придёт сюда автоматически.", result.TicketID)
					}
					if sendErr := sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), reply); sendErr != nil {
						logger.Error("MAX reply failed", "error", sendErr)
					}
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			logger.Error("MAX polling failed", "error", err)
			waitContext(ctx, 3*time.Second)
		}
	}
}
