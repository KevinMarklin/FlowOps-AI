package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"flowops-ai/internal/channels"
	"flowops-ai/internal/delivery"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
)

var errDialogueClosed = errors.New("Обращение закрыто. Создайте новое обращение.")
var errDialogueInput = errors.New("Укажите сообщение до 6000 символов и уникальный идентификатор отправки.")
var errRatingInput = errors.New("Оценка должна быть от 1 до 5, комментарий — до 1000 символов.")

type dialogueActor struct {
	id, sender string
	operator   bool
}

func webDialogueActor(ctx context.Context) dialogueActor {
	p, _ := security.CurrentPrincipal(ctx)
	return dialogueActor{id: p.ID, operator: p.Role == "operator" || p.Role == "admin"}
}

func (h *TicketsHandler) lockDialogue(ctx context.Context, tx pgx.Tx, id int, actor dialogueActor) (models.Ticket, error) {
	var t models.Ticket
	err := tx.QueryRow(ctx, `SELECT id,raw_text,department,priority,extracted_summary,rag_answer,operator_reply,status,created_at,COALESCE(channel,'web'),COALESCE(user_email,''),COALESCE(callback_url,'') FROM tickets WHERE tenant_id=$1 AND id=$2 AND ($3 OR ($4<>'' AND requester_id=$4) OR ($4='' AND $5<>'' AND requester_id='' AND channel='bot' AND user_email=$5)) FOR UPDATE`, security.TenantID(ctx), id, actor.operator, actor.id, actor.sender).Scan(&t.ID, &t.RawText, &t.Department, &t.Priority, &t.ExtractedSummary, &t.RAGAnswer, &t.OperatorReply, &t.Status, &t.CreatedAt, &t.Channel, &t.UserEmail, &t.CallbackURL)
	return t, err
}

func dialogueClosed(status string) bool { return status == "closed" || status == "auto_resolved" }

func (h *TicketsHandler) seedDialogue(ctx context.Context, tx pgx.Tx, t models.Ticket) error {
	for _, entry := range []struct{ key, role, body string }{
		{"initial", "employee", t.RawText},
		{"initial-answer", "assistant", stringValue(t.RAGAnswer)},
		{"legacy-reply", "operator", stringValue(t.OperatorReply)},
	} {
		if strings.TrimSpace(entry.body) == "" {
			continue
		}
		// A legacy reply is only imported if no operator message was recorded yet.
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,role,body,source_key,created_at) SELECT $1,$2,$3,$4,$5,$6 WHERE $5='initial' OR NOT EXISTS(SELECT 1 FROM ticket_messages WHERE tenant_id=$1 AND ticket_id=$2 AND role=$3) ON CONFLICT(tenant_id,ticket_id,source_key) DO NOTHING`, security.TenantID(ctx), t.ID, entry.role, entry.body, entry.key, t.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func actorKey(a dialogueActor) string {
	if a.id != "" {
		return a.id
	}
	return a.sender
}

func (h *TicketsHandler) readDialogue(ctx context.Context, id int, a dialogueActor, before int64) (models.Ticket, error) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return models.Ticket{}, err
	}
	defer tx.Rollback(ctx)
	t, err := h.lockDialogue(ctx, tx, id, a)
	if err != nil {
		return t, err
	}
	if err = h.seedDialogue(ctx, tx, t); err != nil {
		return t, err
	}
	rows, err := tx.Query(ctx, `SELECT id,role,body,created_at,score,comment FROM (SELECT m.id,m.role,m.body,m.created_at,COALESCE(r.score,0) score,COALESCE(r.comment,'') comment FROM ticket_messages m LEFT JOIN LATERAL (SELECT score,comment FROM ticket_answer_ratings WHERE message_id=m.id AND ($5 OR actor=$3) ORDER BY updated_at DESC LIMIT 1) r ON TRUE WHERE m.tenant_id=$1 AND m.ticket_id=$2 AND ($4::bigint=0 OR m.id<$4) ORDER BY m.id DESC LIMIT 100) page ORDER BY id`, security.TenantID(ctx), id, actorKey(a), before, a.operator)
	if err != nil {
		return t, err
	}
	t.Messages = []models.TicketMessage{}
	for rows.Next() {
		var m models.TicketMessage
		if err = rows.Scan(&m.ID, &m.Role, &m.Body, &m.CreatedAt, &m.Rating, &m.RatingComment); err != nil {
			rows.Close()
			return t, err
		}
		t.Messages = append(t.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

func (h *TicketsHandler) Messages(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	id, ok := ticketID(r)
	if !ok {
		writeError(w, 400, "Некорректный номер обращения")
		return
	}
	actor := webDialogueActor(ctx)
	if r.Method == http.MethodGet {
		before := int64(0)
		var err error
		if raw := r.URL.Query().Get("before"); raw != "" {
			before, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || before < 0 {
				writeError(w, 400, "Некорректная страница")
				return
			}
		}
		t, err := h.readDialogue(ctx, id, actor, before)
		if err != nil {
			writeDialogueError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ticket_id": id, "status": t.Status, "messages": t.Messages, "can_reply": !dialogueClosed(t.Status), "has_older": len(t.Messages) == 100})
		return
	}
	var input struct {
		Body string `json:"body"`
		Key  string `json:"client_message_id"`
	}
	if !decodeDialogue(w, r, &input) {
		return
	}
	_, err := h.appendDialogue(ctx, id, actor, input.Body, input.Key, false)
	if err != nil {
		writeDialogueError(w, err)
		return
	}
	t, err := h.readDialogue(ctx, id, actor, 0)
	if err != nil {
		writeDialogueError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"ticket_id": id, "status": t.Status, "messages": t.Messages, "can_reply": !dialogueClosed(t.Status), "has_older": len(t.Messages) == 100})
}

func decodeDialogue(w http.ResponseWriter, r *http.Request, value any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "Некорректные данные запроса")
		return false
	}
	return true
}

func writeDialogueError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, 404, "Обращение или ответ не найден")
	case errors.Is(err, errDialogueClosed), errors.Is(err, errRequestConflict):
		writeError(w, 409, err.Error())
	case errors.Is(err, errDialogueInput), errors.Is(err, errRatingInput):
		writeError(w, 400, err.Error())
	default:
		writeError(w, 503, "Не удалось сохранить переписку. Повторите попытку.")
	}
}

// The ticket lock serializes messages, decisions and retries across all channels.
func (h *TicketsHandler) appendDialogue(ctx context.Context, id int, a dialogueActor, body, key string, bot bool) (models.Ticket, error) {
	body = strings.TrimSpace(body)
	if body == "" || len([]rune(body)) > 6000 || len(body) > security.MaxTicketBytes || !requestKeyPattern.MatchString(key) {
		return models.Ticket{}, errDialogueInput
	}
	// Model/configuration calls happen before taking the write lock. A later
	// operator reply or closure is checked again inside the transaction.
	var preparedAnswer *string
	if !a.operator {
		previous, readErr := h.readDialogue(ctx, id, a, 0)
		if readErr != nil {
			return previous, readErr
		}
		negative := strings.Contains(strings.ToLower(body), "не помог") || strings.Contains(strings.ToLower(body), "не сработ")
		if !negative && !dialogueClosed(previous.Status) && previous.Status != "assigned_to_operator" && previous.OperatorReply == nil {
			contextText := body
			for i := len(previous.Messages) - 1; i >= 0; i-- {
				message := previous.Messages[i]
				if message.Role == "employee" && len(message.Body)+len(contextText)+2 <= security.MaxTicketBytes {
					contextText = message.Body + "\n\n" + contextText
					break
				}
			}
			preparedAnswer = h.analyzer.Analyze(ctx, contextText).RAGAnswer
		}
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return models.Ticket{}, err
	}
	defer tx.Rollback(ctx)
	t, err := h.lockDialogue(ctx, tx, id, a)
	if err != nil {
		return t, err
	}
	if err = h.seedDialogue(ctx, tx, t); err != nil {
		return t, err
	}
	role := "employee"
	if a.operator {
		role = "operator"
	}
	source := "message:" + requestHash([]byte(actorKey(a)+":"+key))
	var previous string
	err = tx.QueryRow(ctx, `SELECT body FROM ticket_messages WHERE tenant_id=$1 AND ticket_id=$2 AND source_key=$3`, security.TenantID(ctx), id, source).Scan(&previous)
	if err == nil {
		if previous != body {
			return t, errRequestConflict
		}
		return t, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return t, err
	}
	if dialogueClosed(t.Status) {
		return t, errDialogueClosed
	}
	var messageID int64
	err = tx.QueryRow(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,role,actor,body,source_key) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, security.TenantID(ctx), id, role, actorKey(a), body, source).Scan(&messageID)
	if err != nil {
		return t, err
	}
	if a.operator {
		if err = h.callbacks.Validate(t.CallbackURL); err != nil {
			return t, err
		}
		t.OperatorReply = &body
		t.Status = "awaiting_confirmation"
		if _, err = tx.Exec(ctx, `UPDATE tickets SET operator_reply=$3,status=$4 WHERE tenant_id=$1 AND id=$2`, security.TenantID(ctx), id, body, t.Status); err != nil {
			return t, err
		}
		if err = delivery.EnqueueConversationMessage(ctx, tx, id, messageID, t.Channel, t.UserEmail, t.CallbackURL, body); err != nil {
			return t, err
		}
	} else {
		// Never interrupt an operator's conversation with a repeated automated answer.
		var operatorPresent bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_messages WHERE tenant_id=$1 AND ticket_id=$2 AND role='operator')`, security.TenantID(ctx), id).Scan(&operatorPresent); err != nil {
			return t, err
		}
		if !operatorPresent && t.Status != "assigned_to_operator" {
			if preparedAnswer != nil {
				answer := *preparedAnswer
				if _, err = tx.Exec(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,role,body,source_key) VALUES($1,$2,'assistant',$3,$4)`, security.TenantID(ctx), id, answer, "answer:"+source); err != nil {
					return t, err
				}
				t.RAGAnswer = &answer
				t.Status = "awaiting_confirmation"
			} else {
				t.Status = "assigned_to_operator"
			}
		} else {
			t.Status = "assigned_to_operator"
		}
		if _, err = tx.Exec(ctx, `UPDATE tickets SET status=$3,rag_answer=$4 WHERE tenant_id=$1 AND id=$2`, security.TenantID(ctx), id, t.Status, t.RAGAnswer); err != nil {
			return t, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,ticket_id,details) VALUES($1,'TICKET_MESSAGE',$2,$3,$4)`, security.TenantID(ctx), actorKey(a), id, role); err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

func (h *TicketsHandler) Decision(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	if !ok {
		writeError(w, 400, "Некорректный номер")
		return
	}
	var input struct {
		Resolved bool `json:"resolved"`
	}
	if !decodeDialogue(w, r, &input) {
		return
	}
	a := webDialogueActor(r.Context())
	a.operator = false
	t, err := h.decideDialogue(r.Context(), id, a, input.Resolved)
	if err != nil {
		writeDialogueError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"status": t.Status})
}

func (h *TicketsHandler) decideDialogue(ctx context.Context, id int, a dialogueActor, resolved bool) (models.Ticket, error) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return models.Ticket{}, err
	}
	defer tx.Rollback(ctx)
	t, err := h.lockDialogue(ctx, tx, id, a)
	if err != nil {
		return t, err
	}
	if dialogueClosed(t.Status) {
		if resolved {
			return t, nil
		}
		return t, errDialogueClosed
	}
	if err = h.seedDialogue(ctx, tx, t); err != nil {
		return t, err
	}
	t.Status = "assigned_to_operator"
	text := "Ответ не помог. Диалог остаётся открытым. Сообщите, что не получилось."
	if resolved {
		t.Status = "closed"
		if t.RAGAnswer != nil && t.OperatorReply == nil {
			t.Status = "auto_resolved"
		}
		text = "Сотрудник подтвердил: вопрос решён."
	}
	if _, err = tx.Exec(ctx, `UPDATE tickets SET status=$3,requester_closed=$4 WHERE tenant_id=$1 AND id=$2`, security.TenantID(ctx), id, t.Status, resolved); err != nil {
		return t, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,role,body,source_key) VALUES($1,$2,'system',$3,$4) ON CONFLICT(tenant_id,ticket_id,source_key) DO NOTHING`, security.TenantID(ctx), id, text, fmt.Sprintf("decision:%s:%s", actorKey(a), t.Status)); err != nil {
		return t, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,ticket_id,details) VALUES($1,'TICKET_FEEDBACK',$2,$3,$4)`, security.TenantID(ctx), actorKey(a), id, "status="+t.Status); err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

func (h *TicketsHandler) RateAnswer(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	message, err := strconv.ParseInt(r.PathValue("message"), 10, 64)
	if !ok || err != nil || message <= 0 {
		writeError(w, 400, "Некорректный ответ")
		return
	}
	var input struct {
		Score   int    `json:"score"`
		Comment string `json:"comment"`
	}
	if !decodeDialogue(w, r, &input) {
		return
	}
	a := webDialogueActor(r.Context())
	a.operator = false
	if err = h.rateDialogue(r.Context(), id, message, a, input.Score, input.Comment); err != nil {
		writeDialogueError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"score": input.Score})
}

func (h *TicketsHandler) rateDialogue(ctx context.Context, id int, message int64, a dialogueActor, score int, comment string) error {
	if score < 1 || score > 5 || len([]rune(comment)) > 1000 {
		return errRatingInput
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = h.lockDialogue(ctx, tx, id, a); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_messages WHERE tenant_id=$1 AND ticket_id=$2 AND id=$3 AND role IN ('assistant','operator'))`, security.TenantID(ctx), id, message).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ticket_answer_ratings(message_id,actor,score,comment) VALUES($1,$2,$3,$4) ON CONFLICT(message_id,actor) DO UPDATE SET score=EXCLUDED.score,comment=EXCLUDED.comment,updated_at=NOW()`, message, actorKey(a), score, strings.TrimSpace(comment)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Bot operations resolve account links on every request, including old callbacks.
func (h *TicketsHandler) botDialogueActor(ctx context.Context, sender string) (context.Context, dialogueActor, error) {
	if security.BotSenderChannel(sender) == "" {
		return ctx, dialogueActor{}, pgx.ErrNoRows
	}
	ctx, err := security.BotContext(ctx, h.db, sender)
	return ctx, dialogueActor{id: security.RequesterID(ctx), sender: sender}, err
}

func (h *TicketsHandler) BotActivate(ctx context.Context, sender string, id int) error {
	original := security.TenantID(ctx)
	linked, a, err := h.botDialogueActor(ctx, sender)
	if err != nil {
		return err
	}
	if id > 0 {
		if _, err = h.readDialogue(linked, id, a, 0); err != nil {
			return err
		}
	}
	_, err = h.db.Exec(ctx, `INSERT INTO bot_dialogues(bot_tenant_id,sender,ticket_id) VALUES($1,$2,NULLIF($3,0)) ON CONFLICT(bot_tenant_id,sender) DO UPDATE SET ticket_id=EXCLUDED.ticket_id`, original, sender, id)
	return err
}

func (h *TicketsHandler) BotContinue(ctx context.Context, sender, body, key string) (models.Ticket, error) {
	linked, a, resolveErr := h.botDialogueActor(ctx, sender)
	if resolveErr != nil {
		return models.Ticket{}, resolveErr
	}
	var receipt int
	receiptErr := h.db.QueryRow(ctx, `SELECT ticket_id FROM bot_dialogue_receipts WHERE bot_tenant_id=$1 AND sender=$2 AND key_hash=$3`, security.TenantID(ctx), sender, requestHash([]byte(key))).Scan(&receipt)
	if receiptErr == nil {
		return h.readDialogue(linked, receipt, a, 0)
	}
	if !errors.Is(receiptErr, pgx.ErrNoRows) {
		return models.Ticket{}, receiptErr
	}
	var id *int
	err := h.db.QueryRow(ctx, `SELECT ticket_id FROM bot_dialogues WHERE bot_tenant_id=$1 AND sender=$2`, security.TenantID(ctx), sender).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var latest int
		err = h.db.QueryRow(linked, `SELECT id FROM tickets WHERE tenant_id=$1 AND (($2<>'' AND requester_id=$2) OR ($2='' AND requester_id='' AND channel='bot' AND user_email=$3)) AND status NOT IN ('closed','auto_resolved') ORDER BY id DESC LIMIT 1`, security.TenantID(linked), a.id, sender).Scan(&latest)
		if err == nil {
			id = &latest
			if err = h.BotActivate(ctx, sender, latest); err != nil {
				return models.Ticket{}, err
			}
		}
	}
	if errors.Is(err, pgx.ErrNoRows) || err == nil && id == nil {
		return models.Ticket{}, channels.ErrNoActiveDialogue
	}
	if err != nil {
		return models.Ticket{}, err
	}
	if _, err = h.appendDialogue(linked, *id, a, body, key, true); err != nil {
		if errors.Is(err, errDialogueClosed) {
			_ = h.BotActivate(ctx, sender, 0)
			return models.Ticket{}, channels.ErrNoActiveDialogue
		}
		return models.Ticket{}, err
	}
	return h.readDialogue(linked, *id, a, 0)
}

func (h *TicketsHandler) BotDecide(ctx context.Context, sender string, id int, resolved bool) (models.Ticket, error) {
	linked, a, err := h.botDialogueActor(ctx, sender)
	if err != nil {
		return models.Ticket{}, err
	}
	if _, err = h.decideDialogue(linked, id, a, resolved); err != nil {
		if errors.Is(err, errDialogueClosed) {
			return models.Ticket{}, channels.ErrBotFeedbackClosed
		}
		return models.Ticket{}, err
	}
	return h.readDialogue(linked, id, a, 0)
}

func (h *TicketsHandler) BotRate(ctx context.Context, sender string, id int, message int64, score int) error {
	linked, a, err := h.botDialogueActor(ctx, sender)
	if err != nil {
		return err
	}
	return h.rateDialogue(linked, id, message, a, score, "")
}
