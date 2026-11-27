package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	Callback *struct {
		ID      string `json:"callback_id"`
		Payload string `json:"payload"`
		User    struct {
			UserID int64 `json:"user_id"`
		} `json:"user"`
	} `json:"callback"`
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

func sendMaxScreen(ctx context.Context, cfg Config, userID int64, screen telegramScreen) error {
	buttons := [][]map[string]string{}
	for _, row := range screen.Buttons {
		converted := []map[string]string{}
		for _, b := range row {
			converted = append(converted, map[string]string{"type": "callback", "text": b.Text, "payload": b.Data})
		}
		buttons = append(buttons, converted)
	}
	text := telegramTextPages(screen.Text, 3500)[0]
	body := map[string]any{"text": FormatMAXMarkdown(text), "attachments": []any{map[string]any{"type": "inline_keyboard", "payload": map[string]any{"buttons": buttons}}}}
	resp, err := maxRequest(ctx, cfg, http.MethodPost, "/messages", url.Values{"user_id": {strconv.FormatInt(userID, 10)}}, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MAX screen returned %s", resp.Status)
	}
	return nil
}

func handleMaxCallback(ctx context.Context, cfg Config, update maxUpdate, service EmployeeTickets) error {
	if update.Callback == nil || update.Callback.User.UserID <= 0 || strings.EqualFold(update.Message.Recipient.ChatType, "chat") || strings.EqualFold(update.Message.Recipient.ChatType, "channel") {
		return nil
	}
	callback := update.Callback
	user := callback.User.UserID
	sender := fmt.Sprintf("max:%d", user)
	resp, err := maxRequest(ctx, cfg, http.MethodPost, "/answers", url.Values{"callback_id": {callback.ID}}, map[string]string{"notification": "Принято"})
	if err == nil {
		resp.Body.Close()
	}
	action := callback.Payload
	if len(action) > 128 {
		return nil
	}
	if strings.HasPrefix(action, "emp:rate:") {
		parts := strings.Split(action, ":")
		if len(parts) != 5 {
			return nil
		}
		id, e1 := strconv.Atoi(parts[2])
		message, e2 := strconv.ParseInt(parts[3], 10, 64)
		score, e3 := strconv.Atoi(parts[4])
		dialogue, ok := service.(EmployeeDialogue)
		if !ok || e1 != nil || e2 != nil || e3 != nil {
			return nil
		}
		if err = dialogue.BotRate(ctx, sender, id, message, score); err != nil {
			return sendMaxMessage(ctx, cfg, "user_id", strconv.FormatInt(user, 10), "Не удалось оценить ответ. Откройте своё обращение заново.")
		}
		action = fmt.Sprintf("emp:ticket:%d", id)
	}
	if action == "emp:new" {
		if dialogue, ok := service.(EmployeeDialogue); ok {
			if err = dialogue.BotActivate(ctx, sender, 0); err != nil {
				return err
			}
		}
	}
	return sendMaxScreen(ctx, cfg, user, employeeAction(ctx, sender, action, service, cfg.BuilderConfig))
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
		query := url.Values{"timeout": {"30"}, "types": {"message_created,bot_started,message_callback"}}
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
						if update.UpdateType == "message_callback" {
							var service EmployeeTickets
							if len(services) > 0 {
								service = services[0]
							}
							if update.Callback != nil {
								drafts.close(update.Callback.User.UserID)
							}
							if err := handleMaxCallback(ctx, cfg, update, service); err != nil {
								logger.Error("MAX callback failed", "error", "operation failed")
							}
							continue
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
							if text == "/new" {
								if dialogue, ok := service.(EmployeeDialogue); ok {
									_ = dialogue.BotActivate(ctx, sender, 0)
								}
								drafts.close(userID)
								_ = sendMaxScreen(ctx, cfg, userID, employeeAction(ctx, sender, "emp:new", service, cfg.BuilderConfig))
								continue
							}
							if text == "/menu" || strings.HasPrefix(text, "/ticket ") {
								action := "emp:menu"
								if strings.HasPrefix(text, "/ticket ") {
									action = "emp:ticket:" + strings.TrimSpace(strings.TrimPrefix(text, "/ticket "))
								}
								_ = sendMaxScreen(ctx, cfg, userID, employeeAction(ctx, sender, action, service, cfg.BuilderConfig))
								continue
							}
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
								if dialogue, ok := service.(EmployeeDialogue); ok {
									ticket, err := dialogue.BotDecide(ctx, sender, id, resolved)
									if err == nil {
										_ = sendMaxScreen(ctx, cfg, userID, employeeTicketScreen(ticket, 0))
									} else {
										_ = sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), "Не удалось изменить обращение. Проверьте номер и состояние.")
									}
									continue
								}
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
						if privateUser && drafts.get(userID) == nil {
							if dialogue, ok := service.(EmployeeDialogue); ok {
								ticket, err := dialogue.BotContinue(ctx, sender, text, request.IdempotencyKey)
								if err == nil {
									_ = sendMaxScreen(ctx, cfg, userID, employeeDialogueUpdate(ticket))
									continue
								}
								if !errors.Is(err, ErrNoActiveDialogue) {
									receipts.forget(receiptKey)
									_ = sendMaxMessage(ctx, cfg, targetType, strconv.FormatInt(targetID, 10), "Не удалось добавить сообщение. Проверьте, что обращение открыто, и повторите отправку.")
									continue
								}
							}
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
						// Every consumed clarification ID belongs to the same creation.
						request.BotMessageKeys = intakeMessageKeys(draft, request.IdempotencyKey)
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
						if privateUser {
							if dialogue, ok := service.(EmployeeDialogue); ok {
								if err := dialogue.BotActivate(ctx, sender, result.TicketID); err == nil {
									if ticket, err := service.BotTicket(ctx, sender, result.TicketID); err == nil {
										_ = sendMaxScreen(ctx, cfg, userID, employeeTicketScreen(ticket, 0))
										continue
									}
								}
							}
						}
						reply := intakeReceipt(draft) + result.ReplyText
						if privateUser && result.AutoReplied && result.Status == "awaiting_confirmation" {
							reply += fmt.Sprintf("\n\nВопрос решён: /yes %d\nНе помогло: /no %d", result.TicketID, result.TicketID)
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
