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

func handleTelegramEmployee(ctx context.Context, update telegramUpdate, service EmployeeTickets, loader settings.Loader, ingest IngestFunc, command telegramCommandFunc) error {
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
		screen := employeeAction(ctx, fmt.Sprintf("tg:%d", chatID), action, service, loader)
		return command(ctx, "sendMessage", telegramScreenBody(chatID, screen))
	}
	if update.Message.Chat.Type != "private" || chatID <= 0 || update.Message.From.ID != chatID {
		return nil
	}
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return nil
	}
	if reply, handled := botLinkCommand(ctx, fmt.Sprintf("tg:%d", chatID), text, service); handled {
		return command(ctx, "sendMessage", telegramScreenBody(chatID, telegramScreen{Text: reply, Buttons: [][]telegramButton{menuButton()}}))
	}
	if strings.HasPrefix(text, "/") {
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
	_ = command(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"})
	result, err := ingest(ctx, models.IngestRequest{Channel: "bot", Sender: fmt.Sprintf("tg:%d", chatID), Message: text, RequireConfirmation: true})
	if err != nil {
		return command(ctx, "sendMessage", telegramScreenBody(chatID, telegramScreen{Text: "Не удалось отправить обращение. Попробуйте ещё раз позже.", Buttons: [][]telegramButton{menuButton()}}))
	}
	text = fmt.Sprintf("Обращение #%d принято\nОтдел: %s\nПриоритет: %s\nСтатус: %s\n\n", result.TicketID, shortTelegramText(botDepartment(result.Department), 120), botPriority(result.Priority), botStatus(result.Status)) + result.ReplyText
	if result.AutoReplied {
		text = strings.TrimSuffix(text, "\n\nЕсли решение не помогло, просто напишите сюда ещё раз — мы подключим специалиста.")
	} else {
		text += "\n\nОтвет специалиста придёт в этот чат. Повторно отправлять обращение не нужно."
	}
	screen := employeeReplyScreen(result.TicketID, text)
	if result.AutoReplied && result.Status == "awaiting_confirmation" {
		screen.Buttons = append([][]telegramButton{{{"Помогло", fmt.Sprintf("emp:yes:%d", result.TicketID)}, {"Нужен специалист", fmt.Sprintf("emp:no:%d", result.TicketID)}}}, screen.Buttons...)
	}
	return command(ctx, "sendMessage", telegramScreenBody(chatID, screen))
}
