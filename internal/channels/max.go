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
	"flowops-ai/internal/security"
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
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("MAX endpoint must use HTTPS")
	}
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
	resp, err := maxRequest(ctx, cfg, http.MethodPost, "/messages", query, map[string]string{"text": FormatMAXMarkdown(text)})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MAX send message returned %s", resp.Status)
	}
	return nil
}

func runMax(ctx context.Context, cfg Config, ingest IngestFunc, logger interface {
	Info(string, ...any)
	Error(string, ...any)
}) {
	ctx = security.WithTenantID(ctx, cfg.TenantID)
	cfg.updateStatus("max", "starting", "")
	var marker int64
	for ctx.Err() == nil {
		query := url.Values{"timeout": {"30"}, "types": {"message_created,bot_started"}}
		if marker > 0 {
			query.Set("marker", strconv.FormatInt(marker, 10))
		}
		resp, err := maxRequest(ctx, cfg, http.MethodGet, "/updates", query, nil)
		if err == nil {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				err = fmt.Errorf("MAX polling returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
			} else {
				var updates []maxUpdate
				var payload struct {
					Updates []maxUpdate `json:"updates"`
					Marker  int64       `json:"marker"`
				}
				err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload)
				resp.Body.Close()
				updates = payload.Updates
				if err == nil {
					if payload.Marker > marker {
						marker = payload.Marker
					}
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
								builderConfig := cfg.builderConfig(ctx)
								if sendErr := sendMaxMessage(ctx, cfg, target, strconv.FormatInt(targetID, 10), builderConfig.StartTemplate); sendErr != nil {
									logger.Error("MAX welcome failed", "error", "operation failed")
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
						if text == "/start" || strings.HasPrefix(text, "/start@") || strings.HasPrefix(text, "/start ") {
							userID := update.Message.Sender.UserID
							chatID := update.Message.Recipient.ChatID
							if chatID == 0 {
								chatID = update.ChatID
							}
							target, targetID := "user_id", userID
							if targetID == 0 {
								target, targetID = "chat_id", chatID
							}
							if targetID != 0 {
								builderConfig := cfg.builderConfig(ctx)
								if sendErr := sendMaxMessage(ctx, cfg, target, strconv.FormatInt(targetID, 10), builderConfig.StartTemplate); sendErr != nil {
									logger.Error("MAX welcome failed", "error", "operation failed")
								}
							}
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
							logger.Error("MAX ingest failed", "error", "operation failed")
							continue
						}
						reply := result.ReplyText
						if sendErr := sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), reply); sendErr != nil {
							LogOutgoing(logger, "bot", sender, result.TicketID, "AI", sendErr)
						} else {
							LogOutgoing(logger, "bot", sender, result.TicketID, "AI", nil)
						}
					}
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			cfg.updateStatus("max", "error", err.Error())
			logger.Error("MAX polling failed", "error", err.Error())
			waitContext(ctx, 3*time.Second)
		} else if err == nil {
			cfg.updateStatus("max", "connected", "")
		}
	}
}
