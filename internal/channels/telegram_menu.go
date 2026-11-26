package channels

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"flowops-ai/internal/models"
	"flowops-ai/internal/settings"
)

type telegramButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}
type telegramScreen struct {
	Text    string
	Buttons [][]telegramButton
}

func menuButton() []telegramButton { return []telegramButton{{"Меню", "emp:menu"}} }

func employeeReplyScreen(ticketID int, text string) telegramScreen {
	// A single notification avoids flooding the chat and partial multi-message
	// retries. Full text is available through the paged ticket screen.
	pages := telegramTextPages(text, 3500)
	screen := telegramScreen{Text: pages[0]}
	if len(pages) > 1 {
		screen.Text += "…\n\nПолный текст доступен в карточке обращения в «Мои обращения»."
	}
	if ticketID > 0 {
		screen.Buttons = append(screen.Buttons, []telegramButton{{"Открыть обращение", fmt.Sprintf("emp:ticket:%d", ticketID)}})
	}
	screen.Buttons = append(screen.Buttons, menuButton())
	return screen
}

func employeeMainMenu(ctx context.Context, loader settings.Loader) telegramScreen {
	return telegramScreen{Text: shortTelegramText(StartReplyText(ctx, loader), 1500) + "\n\nМеню сотрудника\nОтправьте текст проблемы или выберите действие. После привязки аккаунта доступны также ваши обращения с сайта.", Buttons: [][]telegramButton{
		{{"Новое обращение", "emp:new"}},
		{{"Мои обращения", "emp:list:0"}, {"Найти по номеру", "emp:find"}},
		{{"Как работает поддержка", "emp:help"}},
		{{"Связать с аккаунтом сайта", "emp:account"}},
	}}
}

func employeeAction(ctx context.Context, sender, action string, service EmployeeTickets, loader settings.Loader) telegramScreen {
	plain := func(text string) telegramScreen {
		return telegramScreen{Text: text, Buttons: [][]telegramButton{menuButton()}}
	}
	switch action {
	case "emp:menu":
		return employeeMainMenu(ctx, loader)
	case "emp:account":
		return plain("Войдите на сайт, откройте меню пользователя (три полоски справа сверху), выберите Telegram и получите одноразовый код. Затем отправьте сюда команду /link КОД. Пароль и код активации сайта присылать боту не нужно.")
	case "emp:new":
		return telegramScreen{Text: "Опишите проблему одним сообщением: что случилось, какую ошибку видите и что уже пробовали.\n\nМы определим отдел и приоритет, найдём регламент или передадим вопрос специалисту.", Buttons: [][]telegramButton{{{"Отмена", "emp:menu"}}}}
	case "emp:find":
		return plain("Чтобы открыть своё обращение, отправьте команду с номером, например:\n/ticket 123\n\nДоступны только обращения, созданные в этом личном чате.")
	case "emp:help":
		return plain("Как работает поддержка\n\n1. Опишите проблему. ИИ выделит суть, определит нужный отдел и приоритет: низкий, средний или высокий.\n2. Если в базе знаний есть регламент, бот покажет его. Нажмите «Помогло» или «Нужен специалист».\n3. Если регламента нет или он не помог, вопрос передаётся специалисту. Ответ придёт в этот чат.\n\nВ интернете ответы не ищем. Историю и текущий статус можно открыть в «Мои обращения».")
	}
	if service == nil {
		return plain("История временно недоступна. Попробуйте открыть меню позже.")
	}
	parts := strings.Split(action, ":")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "emp" {
		return plain("Эта кнопка больше не поддерживается. Откройте меню.")
	}
	id, err := strconv.Atoi(parts[2])
	if err != nil || id < 0 {
		return plain("Некорректный номер обращения. Откройте меню.")
	}
	if parts[1] == "list" && len(parts) == 3 {
		tickets, err := service.BotTickets(ctx, sender, id)
		if err != nil {
			return plain("Не удалось загрузить обращения. Попробуйте ещё раз позже.")
		}
		if len(tickets) == 0 {
			return telegramScreen{Text: "Обращений на этой странице пока нет. Чтобы создать новое, отправьте текст проблемы.", Buttons: [][]telegramButton{{{"Новое обращение", "emp:new"}}, menuButton()}}
		}
		screen := telegramScreen{Text: "Мои обращения\nВыберите обращение, чтобы посмотреть статус, регламент или ответ специалиста."}
		for i, ticket := range tickets {
			if i == 8 {
				screen.Buttons = append(screen.Buttons, []telegramButton{{"Более ранние", fmt.Sprintf("emp:list:%d", tickets[7].ID)}})
				break
			}
			summary := ticket.RawText
			if ticket.ExtractedSummary != nil {
				summary = *ticket.ExtractedSummary
			}
			label := fmt.Sprintf("#%d · %s · %s", ticket.ID, botStatus(ticket.Status), shortTelegramText(strings.Join(strings.Fields(summary), " "), 38))
			screen.Buttons = append(screen.Buttons, []telegramButton{{label, fmt.Sprintf("emp:ticket:%d", ticket.ID)}})
		}
		if id > 0 {
			screen.Buttons = append(screen.Buttons, []telegramButton{{"К последним обращениям", "emp:list:0"}})
		}
		screen.Buttons = append(screen.Buttons, menuButton())
		return screen
	}
	if id <= 0 {
		return plain("Некорректный номер обращения. Откройте меню.")
	}
	page := 0
	if len(parts) == 4 {
		page, err = strconv.Atoi(parts[3])
		if err != nil || page < 0 || page > 1000 {
			return plain("Некорректная страница обращения.")
		}
	}
	var ticket models.Ticket
	switch parts[1] {
	case "ticket":
		ticket, err = service.BotTicket(ctx, sender, id)
	case "yes", "no":
		if len(parts) != 3 {
			return plain("Некорректная кнопка. Откройте обращение заново.")
		}
		ticket, err = service.BotFeedback(ctx, sender, id, parts[1] == "yes")
		if err == ErrBotFeedbackClosed {
			return plain("Ответ уже обработан или обращение закрыто. Откройте его заново в «Мои обращения».")
		}
	default:
		return plain("Эта кнопка больше не поддерживается. Откройте меню.")
	}
	if err != nil {
		return plain("Обращение недоступно. Проверьте номер: можно открыть только свои обращения из этого чата.")
	}
	return employeeTicketScreen(ticket, page)
}

func employeeTicketScreen(ticket models.Ticket, page int) telegramScreen {
	summary := ticket.RawText
	if ticket.ExtractedSummary != nil {
		summary = *ticket.ExtractedSummary
	}
	detail := "Суть: " + summary + "\n\nИсходное обращение:\n" + ticket.RawText
	if ticket.OperatorReply != nil && strings.TrimSpace(*ticket.OperatorReply) != "" {
		detail += "\n\nОтвет специалиста:\n" + *ticket.OperatorReply
	} else if ticket.RAGAnswer != nil && strings.TrimSpace(*ticket.RAGAnswer) != "" {
		detail += "\n\nОтвет по регламенту:\n" + stripKnowledgeSource(*ticket.RAGAnswer)
	} else {
		detail += "\n\nГотового регламента нет. Обращение передано специалисту, ответ появится здесь."
	}
	chunks := telegramTextPages(detail, 2500)
	if page >= len(chunks) {
		page = 0
	}
	header := fmt.Sprintf("Обращение #%d\nСтатус: %s\nОтдел: %s\nПриоритет: %s\n\n", ticket.ID, botStatus(ticket.Status), shortTelegramText(botDepartment(ticket.Department), 120), botPriority(ticket.Priority))
	screen := telegramScreen{Text: header + chunks[page]}
	if len(chunks) > 1 {
		screen.Text += fmt.Sprintf("\n\nСтраница %d из %d", page+1, len(chunks))
		row := []telegramButton{}
		if page > 0 {
			row = append(row, telegramButton{"Назад", fmt.Sprintf("emp:ticket:%d:%d", ticket.ID, page-1)})
		}
		if page+1 < len(chunks) {
			row = append(row, telegramButton{"Далее", fmt.Sprintf("emp:ticket:%d:%d", ticket.ID, page+1)})
		}
		screen.Buttons = append(screen.Buttons, row)
	}
	if ticket.Status == "awaiting_confirmation" && ticket.RAGAnswer != nil && strings.TrimSpace(*ticket.RAGAnswer) != "" && (ticket.OperatorReply == nil || strings.TrimSpace(*ticket.OperatorReply) == "") {
		screen.Buttons = append(screen.Buttons, []telegramButton{{"Помогло", fmt.Sprintf("emp:yes:%d", ticket.ID)}, {"Нужен специалист", fmt.Sprintf("emp:no:%d", ticket.ID)}})
	}
	screen.Buttons = append(screen.Buttons, []telegramButton{{"Обновить статус", fmt.Sprintf("emp:ticket:%d:%d", ticket.ID, page)}}, []telegramButton{{"Мои обращения", "emp:list:0"}}, menuButton())
	return screen
}

func botStatus(status string) string {
	if label, ok := map[string]string{"new": "Принято", "awaiting_confirmation": "Ждёт подтверждения", "assigned_to_operator": "У специалиста", "auto_resolved": "Решено по регламенту", "resolved": "Ответ специалиста", "closed": "Закрыто"}[status]; ok {
		return label
	}
	return "В обработке"
}
func botPriority(priority string) string {
	if label, ok := map[string]string{"low": "Низкий", "medium": "Средний", "high": "Высокий"}[priority]; ok {
		return label
	}
	return "Средний"
}
func botDepartment(department string) string {
	if label, ok := map[string]string{"IT": "ИТ-поддержка", "HR": "HR и кадры", "Billing": "Финансы и биллинг", "Accounting": "Бухгалтерия", "Facilities": "АХО и офис", "General": "Общий отдел", "Unassigned": "Уточняется"}[department]; ok {
		return label
	}
	return department
}

// Telegram counts UTF-16 code units. Pagination keeps full long answers while
// leaving room for the status header and avoiding broken Unicode sequences.
func telegramTextPages(text string, limit int) []string {
	pages := []string{}
	var page strings.Builder
	units := 0
	for _, r := range text {
		n := utf16.RuneLen(r)
		if units+n > limit && page.Len() > 0 {
			pages = append(pages, page.String())
			page.Reset()
			units = 0
		}
		page.WriteRune(r)
		units += n
	}
	if page.Len() > 0 || len(pages) == 0 {
		pages = append(pages, page.String())
	}
	return pages
}
func shortTelegramText(text string, limit int) string {
	pages := telegramTextPages(text, limit)
	if len(pages) > 1 {
		return pages[0] + "…"
	}
	return text
}
