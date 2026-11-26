package channels

import (
	"context"
	"errors"
	"strings"

	"flowops-ai/internal/models"
)

var ErrBotFeedbackClosed = errors.New("bot ticket no longer accepts feedback")

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
	if command == "/link" && len(fields) == 2 {
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
		return "Не удалось привязать аккаунт. Код мог истечь, быть использован или аккаунт уже привязан. Проверьте подключение в настройках сайта; при необходимости отключите старую привязку и получите новый код.", true
	}
	return "Аккаунт привязан к сайту. Новые обращения из бота появятся в вашей истории на сайте. В Telegram через «Мои обращения» также доступны обращения аккаунта с сайта.", true
}
