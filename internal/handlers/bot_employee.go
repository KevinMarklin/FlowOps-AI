package handlers

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"flowops-ai/internal/channels"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
)

const botTicketColumns = `id,raw_text,department,priority,extracted_summary,rag_answer,operator_reply,status,created_at`

func validTelegramSender(sender string) bool {
	id, err := strconv.ParseInt(strings.TrimPrefix(sender, "tg:"), 10, 64)
	return err == nil && id > 0 && sender == "tg:"+strconv.FormatInt(id, 10)
}

func scanBotTicket(row interface{ Scan(...any) error }) (models.Ticket, error) {
	var ticket models.Ticket
	err := row.Scan(&ticket.ID, &ticket.RawText, &ticket.Department, &ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply, &ticket.Status, &ticket.CreatedAt)
	return ticket, err
}

func (h *TicketsHandler) BotTickets(ctx context.Context, sender string, before int) ([]models.Ticket, error) {
	if security.BotSenderChannel(sender) == "" || before < 0 {
		return nil, pgx.ErrNoRows
	}
	ctx, err := security.BotContext(ctx, h.db, sender)
	if err != nil {
		return nil, err
	}
	rows, err := h.db.Query(ctx, `SELECT `+botTicketColumns+` FROM tickets WHERE tenant_id=$1 AND (($4<>'' AND requester_id=$4) OR ($4='' AND channel='bot' AND user_email=$2 AND requester_id='')) AND ($3=0 OR id<$3) ORDER BY id DESC LIMIT 9`, security.TenantID(ctx), sender, before, security.RequesterID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tickets := make([]models.Ticket, 0, 9)
	for rows.Next() {
		ticket, err := scanBotTicket(rows)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, ticket)
	}
	return tickets, rows.Err()
}

func (h *TicketsHandler) BotTicket(ctx context.Context, sender string, id int) (models.Ticket, error) {
	linked, actor, err := h.botDialogueActor(ctx, sender)
	if err != nil {
		return models.Ticket{}, err
	}
	return h.readDialogue(linked, id, actor, 0)
}

func (h *TicketsHandler) BotFeedback(ctx context.Context, sender string, id int, resolved bool) (models.Ticket, error) {
	if !validTelegramSender(sender) || id <= 0 {
		return models.Ticket{}, pgx.ErrNoRows
	}
	originalContext := ctx
	ctx, resolveErr := security.BotContext(ctx, h.db, sender)
	if resolveErr != nil {
		return models.Ticket{}, resolveErr
	}
	status := "assigned_to_operator"
	if resolved {
		status = "auto_resolved"
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return models.Ticket{}, err
	}
	defer tx.Rollback(ctx)
	ticket, err := scanBotTicket(tx.QueryRow(ctx, `UPDATE tickets SET status=$1 WHERE tenant_id=$2 AND (($5<>'' AND requester_id=$5) OR ($5='' AND channel='bot' AND user_email=$3 AND requester_id='')) AND id=$4 AND status='awaiting_confirmation' AND NULLIF(BTRIM(rag_answer),'') IS NOT NULL AND NULLIF(BTRIM(operator_reply),'') IS NULL RETURNING `+botTicketColumns, status, security.TenantID(ctx), sender, id, security.RequesterID(ctx)))
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		current, readErr := h.BotTicket(originalContext, sender, id)
		if readErr != nil {
			return models.Ticket{}, readErr
		}
		if current.Status == status {
			return current, nil
		}
		return models.Ticket{}, channels.ErrBotFeedbackClosed
	}
	if err != nil {
		return models.Ticket{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,ticket_id,details) VALUES($1,'TICKET_FEEDBACK','employee_bot',$2,$3)`, security.TenantID(ctx), id, "status="+status); err != nil {
		return models.Ticket{}, err
	}
	return ticket, tx.Commit(ctx)
}

func (h *TicketsHandler) LinkBot(ctx context.Context, sender, code string) error {
	return security.LinkBotAccount(ctx, h.db, sender, code)
}

func (h *TicketsHandler) UnlinkBot(ctx context.Context, sender string) error {
	return security.UnlinkBotAccount(ctx, h.db, sender)
}
