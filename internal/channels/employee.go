package channels

import (
	"context"
	"errors"
	"strings"

	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

var ErrBotFeedbackClosed = errors.New("bot ticket no longer accepts feedback")
var ErrNoActiveDialogue = errors.New("no active conversation")

type EmployeeDialogue interface {
	BotActivate(context.Context, string, int) error
	BotContinue(context.Context, string, string, string) (models.Ticket, error)
	BotRate(context.Context, string, int, int64, int) error
	BotDecide(context.Context, string, int, bool) (models.Ticket, error)
}

// EmployeeTickets exposes only requester operations. Implementations must scope
// every read and write to both the tenant context and the verified sender.
type EmployeeTickets interface {
	BotTickets(context.Context, string, int) ([]models.Ticket, error)
	BotTicket(context.Context, string, int) (models.Ticket, error)
	BotFeedback(context.Context, string, int, bool) (models.Ticket, error)
}

type BotAccountLinker interface {
	LinkBot(context.Context, string, string) error
}

type BotAccountUnlinker interface {
	UnlinkBot(context.Context, string) error
}

func botLinkCommand(ctx context.Context, sender, text string, service EmployeeTickets) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	command := strings.Split(fields[0], "@")[0]
	if command != "/link" && !strings.Contains(text, "link_") {
		return "", false
	}
	code := ""
	if (command == "/link" || command == "/start") && len(fields) == 2 && strings.HasPrefix(fields[1], "link_") {
		code = fields[1]
	} else if strings.HasPrefix(fields[0], "link_") && len(fields) == 1 {
		code = fields[0]
	}
	if code == "" {
		return "Откройте настройки пользователя на сайте, получите одноразовый код и отправьте сюда команду /link КОД.", true
	}
	linker, ok := service.(BotAccountLinker)
	if !ok || strings.HasPrefix(sender, "max_chat:") {
		return "Привязка доступна только в личном чате с ботом.", true
	}
	if err := linker.LinkBot(ctx, sender, code); err != nil {
		switch {
		case errors.Is(err, security.ErrBotLinkInvalidCode):
			return "Код истёк или уже использован. Получите новый код в настройках сайта и отправьте команду целиком.", true
		case errors.Is(err, security.ErrBotLinkWorkspace):
			return "Этот бот относится к другой компании. Откройте бота вашей компании или обратитесь к администратору.", true
		case errors.Is(err, security.ErrBotLinkConflict):
			return "Telegram или аккаунт сайта уже связан с другим аккаунтом. Откройте в боте «Подключить аккаунт сайта» → «Отключить этот Telegram» либо отключите прежнюю связь в настройках сайта. Затем получите новый код. Чужая привязка автоматически не заменяется.", true
		}
		return "Не удалось привязать аккаунт. Код мог истечь, быть использован или аккаунт уже привязан. Проверьте подключение в настройках сайта; при необходимости отключите старую привязку и получите новый код.", true
	}
	return "Аккаунт привязан к сайту. Новые обращения из бота появятся в вашей истории на сайте. В Telegram через «Мои обращения» также доступны обращения аккаунта с сайта.", true
}
