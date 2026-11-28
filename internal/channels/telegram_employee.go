package channels

import (
	"context"
	"errors"
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
			if err := dialogue.BotRate(ctx, fmt.Sprintf("tg:%d", chatID), id, message, score); err != nil {
				return command(ctx, "sendMessage", telegramScreenBody(chatID, telegramScreen{Text: "Не удалось оценить ответ. Откройте своё обращение заново.", Buttons: [][]telegramButton{menuButton()}}))
			}
			ticket, err := service.BotTicket(ctx, fmt.Sprintf("tg:%d", chatID), id)
			if err != nil {
				return err
			}
			screen := employeeTicketScreen(ticket, 0)
			screen.KeepHistory = true
			body := telegramScreenBody(chatID, screen)
			if callback.Message.MessageID > 0 {
				body["message_id"] = callback.Message.MessageID
				return command(ctx, "editMessageText", body)
			}
			return command(ctx, "sendMessage", body)
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
		if action == "emp:new" {
			if dialogue, ok := service.(EmployeeDialogue); ok {
				if err := dialogue.BotActivate(ctx, fmt.Sprintf("tg:%d", chatID), 0); err != nil {
					return err
				}
			}
		}
		screen := employeeAction(ctx, fmt.Sprintf("tg:%d", chatID), action, service, loader)
		if _, ok := service.(EmployeeDialogue); ok && (strings.HasPrefix(action, "emp:ticket:") || strings.HasPrefix(action, "emp:yes:") || strings.HasPrefix(action, "emp:no:") || strings.HasPrefix(action, "emp:rating")) {
			screen.KeepHistory = true
		}
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
	if strings.HasPrefix(text, "/") && !(text == "/skip" && drafts.intake.get(chatID) != nil) {
		drafts.close(chatID)
		fields := strings.Fields(text)
		name, _, _ := strings.Cut(fields[0], "@")
		screen := employeeMainMenu(ctx, loader)
		switch name {
		case "/start", "/menu", "/cancel":
		case "/new":
			if dialogue, ok := service.(EmployeeDialogue); ok {
				if err := dialogue.BotActivate(ctx, fmt.Sprintf("tg:%d", chatID), 0); err != nil {
					return err
				}
			}
			screen = employeeAction(ctx, fmt.Sprintf("tg:%d", chatID), "emp:new", service, loader)
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
		err := command(ctx, "sendMessage", telegramScreenBody(chatID, screen))
		if err == nil && name == "/new" {
			drafts.open(chatID)
		}
		return err
	}
	if !drafts.waiting(chatID) {
		if dialogue, ok := service.(EmployeeDialogue); ok {
			key := fmt.Sprintf("tg-message:%d", update.Message.MessageID)
			ticket, err := dialogue.BotContinue(ctx, fmt.Sprintf("tg:%d", chatID), text, key)
			if err == nil {
				return command(ctx, "sendMessage", telegramScreenBody(chatID, employeeDialogueUpdate(ticket)))
			}
			if !errors.Is(err, ErrNoActiveDialogue) {
				return command(ctx, "sendMessage", telegramScreenBody(chatID, telegramScreen{Text: "Не удалось добавить сообщение. Проверьте, что обращение открыто, и повторите отправку.", Buttons: [][]telegramButton{menuButton()}, KeepHistory: true}))
			}
		}
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
	draft, question := drafts.intake.advance(chatID, text, key)
	if question != "" {
		return command(ctx, "sendMessage", telegramScreenBody(chatID, telegramScreen{Text: question, Buttons: [][]telegramButton{{{"↩️ Отмена", "emp:menu"}}}}))
	}
	request := models.IngestRequest{Channel: "bot", Sender: fmt.Sprintf("tg:%d", chatID), Message: text, RequireConfirmation: true, IdempotencyKey: key}
	request.BotMessageKeys = intakeMessageKeys(draft, key)
	if draft != nil {
		request.Message = draft.Message
		request.Intake = &draft.Details
		request.IdempotencyKey = draft.Key
	}
	result, err := ingest(ctx, request)
	if err != nil {
		return command(ctx, "sendMessage", telegramScreenBody(chatID, telegramScreen{Text: "Не удалось отправить обращение. Отправьте текст ещё раз или нажмите «Отмена».", Buttons: [][]telegramButton{{{"↩️ Отмена", "emp:menu"}}}}))
	}
	drafts.close(chatID)
	if dialogue, ok := service.(EmployeeDialogue); ok {
		if err := dialogue.BotActivate(ctx, fmt.Sprintf("tg:%d", chatID), result.TicketID); err != nil {
			return err
		}
		ticket, err := service.BotTicket(ctx, fmt.Sprintf("tg:%d", chatID), result.TicketID)
		if err != nil {
			return err
		}
		screen := employeeTicketScreen(ticket, 0)
		screen.KeepHistory = true
		return command(ctx, "sendMessage", telegramScreenBody(chatID, screen))
	}
	text = fmt.Sprintf("**Обращение #%d принято**\nОтдел: %s\nПриоритет: %s\nСтатус: %s\n\n", result.TicketID, shortTelegramText(botDepartment(result.Department), 120), botPriority(result.Priority), botStatus(result.Status)) + intakeReceipt(draft) + result.ReplyText
	if result.AutoReplied {
		text = strings.TrimSuffix(text, "\n\nЕсли решение не помогло, просто напишите сюда ещё раз — мы подключим специалиста.")
	} else {
		text += "\n\nОтвет специалиста придёт в этот чат. Повторно отправлять обращение не нужно."
	}
	screen := employeeReplyScreen(result.TicketID, text)
	if result.AutoReplied && result.Status == "awaiting_confirmation" {
		screen.Buttons = append([][]telegramButton{{{"Вопрос решён", fmt.Sprintf("emp:yes:%d", result.TicketID)}}, {{"Не помогло", fmt.Sprintf("emp:no:%d", result.TicketID)}}}, screen.Buttons...)
	}
	err = command(ctx, "sendMessage", telegramScreenBody(chatID, screen))
	// Remove the submitted text only after the ticket and its result were saved.
	// A cleanup failure must never invite a duplicate submission.
	if err == nil && update.Message.MessageID > 0 {
		_ = command(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": update.Message.MessageID})
	}
	return err
}

func employeeDialogueUpdate(ticket models.Ticket) telegramScreen {
	screen := employeeTicketScreen(ticket, 0)
	screen.KeepHistory = true
	screen.Text = fmt.Sprintf("Обращение #%d · %s\nСообщение сохранено. Ответ специалиста появится здесь и на сайте.", ticket.ID, botStatus(ticket.Status))
	if len(ticket.Messages) > 0 {
		last := ticket.Messages[len(ticket.Messages)-1]
		if last.Role == "assistant" {
			screen.Text = fmt.Sprintf("Обращение #%d\n\n%s", ticket.ID, stripKnowledgeSource(last.Body))
		}
	}
	return screen
}
