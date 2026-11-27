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
			MID  string `json:"mid"`
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
}, services ...EmployeeTickets) {
	ctx = security.WithTenantID(ctx, cfg.TenantID)
	cfg.updateStatus("max", "starting", "")
	drafts := newIntakeDrafts()
	receipts := newMessageReceipts()
	var marker int64
	for ctx.Err() == nil {
		drafts.purgeExpired()
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
							drafts.close(userID)
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
							if maxHasPrivateUser(update) {
								drafts.close(userID)
							}
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
						privateUser := maxHasPrivateUser(update) && targetType == "user_id"
						if sender == "" {
							continue
						}
						receiptKey := ""
						if update.Message.Body.MID != "" {
							receiptKey = sender + "\x00" + update.Message.Body.MID
						}
						if !receipts.accept(receiptKey) {
							continue
						}
						var service EmployeeTickets
						if len(services) > 0 {
							service = services[0]
						}
						if reply, handled := botLinkCommand(ctx, sender, text, service); handled {
							if privateUser {
								drafts.close(userID)
							}
							if err := sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), reply); err != nil {
								logger.Error("MAX account link response failed", "error", "operation failed")
							}
							continue
						}
						if privateUser {
							if drafts.expired(userID) {
								drafts.close(userID)
								_ = sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), "Время уточнения истекло. Опишите проблему заново.")
								continue
							}
							if text == "/cancel" {
								drafts.close(userID)
								_ = sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), "Обращение отменено. Опишите новую проблему, когда понадобится помощь.")
								continue
							}
							if id, resolved, handled := maxFeedbackCommand(text); handled {
								reply := "Не удалось обновить обращение. Проверьте номер: доступны только ваши обращения."
								if service != nil {
									if _, err := service.BotFeedback(ctx, sender, id, resolved); err == nil {
										if resolved {
											reply = "Спасибо! Решение подтверждено."
										} else {
											reply = "Обращение передано специалисту. Ответ придёт в этот чат."
										}
									}
								}
								_ = sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), reply)
								continue
							}
							if strings.HasPrefix(text, "/") && !(text == "/skip" && drafts.get(userID) != nil) {
								_ = sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), "Опишите проблему сообщением. /cancel — отмена; /skip — пропустить уточнения; /yes НОМЕР и /no НОМЕР — оценить предложенное решение.")
								continue
							}
						}
						request := models.IngestRequest{Channel: "bot", Sender: sender, Message: text, RequireConfirmation: true}
						if update.Message.Body.MID != "" {
							request.IdempotencyKey = "max-message:" + update.Message.Body.MID
						}
						var draft *intakeDraft
						if privateUser {
							var question string
							draft, question = drafts.advance(userID, text, request.IdempotencyKey)
							if question != "" {
								if err := sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), question); err != nil {
									receipts.forget(receiptKey)
									logger.Error("MAX clarification response failed", "error", "operation failed")
								}
								continue
							}
							if draft != nil {
								request.Message = draft.Message
								request.Intake = &draft.Details
								request.IdempotencyKey = draft.Key
							}
						}
						result, ingestErr := ingest(ctx, request)
						if ingestErr != nil {
							receipts.forget(receiptKey)
							logger.Error("MAX ingest failed", "error", "operation failed")
							_ = sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), "Не удалось сохранить обращение. Повторите сообщение или /cancel.")
							continue
						}
						if privateUser {
							drafts.close(userID)
						}
						reply := intakeReceipt(draft) + result.ReplyText
						if privateUser && result.AutoReplied && result.Status == "awaiting_confirmation" {
							reply += fmt.Sprintf("\n\nПомогло: /yes %d\nНужен специалист: /no %d", result.TicketID, result.TicketID)
						}
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

// A missing sender identity must not collapse unrelated chat drafts into key 0.
// Group messages must not alter the same user's private conversation either.
func maxHasPrivateUser(update maxUpdate) bool {
	kind := update.Message.Recipient.ChatType
	return update.Message.Sender.UserID > 0 && !strings.EqualFold(kind, "chat") && !strings.EqualFold(kind, "channel")
}
