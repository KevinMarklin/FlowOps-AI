package channels

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"flowops-ai/internal/models"
	"flowops-ai/internal/settings"
)

type telegramCommandFunc func(context.Context, string, any) error

func handleTelegramEmployee(ctx context.Context, update telegramUpdate, service EmployeeTickets, loader settings.Loader, ingest IngestFunc, command telegramCommandFunc, sessions ...*telegramDrafts) error {
	drafts := newTelegramDrafts()
	if len(sessions) > 0 && sessions[0] != nil {
		drafts = sessions[0]
	}
	chatID := update.Message.Chat.ID
	if update.Callback != nil {
		callback := update.Callback
		// Acknowledge promptly even if a stale/forwarded button cannot be used.
		if err := command(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callback.ID}); err != nil {
			return err
		}
		if callback.Message.Chat.Type != "private" || callback.Message.Chat.ID <= 0 || callback.From.ID != callback.Message.Chat.ID {
			return nil
		}
		chatID = callback.Message.Chat.ID
		action := callback.Data
		if len(action) > 64 {
			action = "invalid"
		}
		if strings.HasPrefix(action, "emp:read:") {
			id, err := strconv.Atoi(strings.TrimPrefix(action, "emp:read:"))
			if err != nil || id <= 0 || service == nil || callback.Message.MessageID <= 0 {
				return nil
			}
			ticket, err := service.BotTicket(ctx, fmt.Sprintf("tg:%d", chatID), id)
			if err != nil || ticket.OperatorReply == nil || strings.TrimSpace(*ticket.OperatorReply) == "" {
				return nil
			}
			return command(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": callback.Message.MessageID})
		}
		drafts.close(chatID)
		screen := employeeAction(ctx, fmt.Sprintf("tg:%d", chatID), action, service, loader)
		body := telegramScreenBody(chatID, screen)
		if callback.Message.MessageID > 0 {
			body["message_id"] = callback.Message.MessageID
			err := command(ctx, "editMessageText", body)
			if err == nil && action == "emp:new" {
				drafts.open(chatID)
			}
			return err
		}
		err := command(ctx, "sendMessage", body)
		if err == nil && action == "emp:new" {
			drafts.open(chatID)
		}
		return err
	}
	if update.Message.Chat.Type != "private" || chatID <= 0 || update.Message.From.ID != chatID {
		return nil
	}
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return nil
	}
	if reply, handled := botLinkCommand(ctx, fmt.Sprintf("tg:%d", chatID), text, service); handled {
		drafts.close(chatID)
		err := command(ctx, "sendMessage", telegramScreenBody(chatID, telegramScreen{Text: reply, Buttons: [][]telegramButton{menuButton()}}))
		if err == nil && update.Message.MessageID > 0 {
			_ = command(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": update.Message.MessageID})
		}
		return err
	}
	if strings.HasPrefix(text, "/") {
		drafts.close(chatID)
		fields := strings.Fields(text)
		name, _, _ := strings.Cut(fields[0], "@")
		screen := employeeMainMenu(ctx, loader)
		switch name {
		case "/start", "/menu", "/cancel":
		case "/help":
			screen = employeeAction(ctx, fmt.Sprintf("tg:%d", chatID), "emp:help", service, loader)
		case "/ticket":
			if len(fields) != 2 {
				screen = employeeAction(ctx, fmt.Sprintf("tg:%d", chatID), "emp:find", service, loader)
			} else if id, err := strconv.Atoi(strings.TrimPrefix(fields[1], "#")); err == nil && id > 0 {
				screen = employeeAction(ctx, fmt.Sprintf("tg:%d", chatID), fmt.Sprintf("emp:ticket:%d", id), service, loader)
			} else {
				screen = employeeAction(ctx, fmt.Sprintf("tg:%d", chatID), "emp:find", service, loader)
			}
		default:
			screen.Text = "Такой команды нет. Выберите действие в меню сотрудника."
		}
		return command(ctx, "sendMessage", telegramScreenBody(chatID, screen))
	}
	if !drafts.waiting(chatID) {
		return command(ctx, "sendMessage", telegramScreenBody(chatID, telegramScreen{
			Text:    "Чтобы отправить обращение, нажмите «✍️ Новое обращение», затем опишите проблему одним сообщением.",
			Buttons: [][]telegramButton{{{"✍️ Новое обращение", "emp:new"}}, menuButton()},
		}))
	}
	_ = command(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"})
	key := ""
	if update.Message.MessageID > 0 {
		key = fmt.Sprintf("tg-message:%d", update.Message.MessageID)
	}
	result, err := ingest(ctx, models.IngestRequest{Channel: "bot", Sender: fmt.Sprintf("tg:%d", chatID), Message: text, RequireConfirmation: true, IdempotencyKey: key})
	if err != nil {
		return command(ctx, "sendMessage", telegramScreenBody(chatID, telegramScreen{Text: "Не удалось отправить обращение. Отправьте текст ещё раз или нажмите «Отмена».", Buttons: [][]telegramButton{{{"↩️ Отмена", "emp:menu"}}}}))
	}
	drafts.close(chatID)
	text = fmt.Sprintf("**Обращение #%d принято**\nОтдел: %s\nПриоритет: %s\nСтатус: %s\n\n", result.TicketID, shortTelegramText(botDepartment(result.Department), 120), botPriority(result.Priority), botStatus(result.Status)) + result.ReplyText
	if result.AutoReplied {
		text = strings.TrimSuffix(text, "\n\nЕсли решение не помогло, просто напишите сюда ещё раз — мы подключим специалиста.")
	} else {
		text += "\n\nОтвет специалиста придёт в этот чат. Повторно отправлять обращение не нужно."
	}
	screen := employeeReplyScreen(result.TicketID, text)
	if result.AutoReplied && result.Status == "awaiting_confirmation" {
		screen.Buttons = append([][]telegramButton{{{"✅ Помогло", fmt.Sprintf("emp:yes:%d", result.TicketID)}}, {{"👤 Нужен специалист", fmt.Sprintf("emp:no:%d", result.TicketID)}}}, screen.Buttons...)
	}
	err = command(ctx, "sendMessage", telegramScreenBody(chatID, screen))
	// Remove the submitted text only after the ticket and its result were saved.
	// A cleanup failure must never invite a duplicate submission.
	if err == nil && update.Message.MessageID > 0 {
		_ = command(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": update.Message.MessageID})
	}
	return err
}
