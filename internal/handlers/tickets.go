package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/channels"
	"flowops-ai/internal/delivery"
	"flowops-ai/internal/intake"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TicketsHandler struct {
	structuredGenerator func(context.Context, string, string) (string, bool, error)
	db                  *pgxpool.Pool
	logger              *slog.Logger
	analyzer            *analyzer.Analyzer
	dispatcher          *channels.Dispatcher
	tenantDispatcher    *channels.TenantDispatcher
	builderConfig       settings.Loader
	callbacks           *security.Callbacks
}

func NewTicketsHandler(db *pgxpool.Pool, logger *slog.Logger, ticketAnalyzer *analyzer.Analyzer, dispatchers ...*channels.Dispatcher) *TicketsHandler {
	handler := &TicketsHandler{db: db, logger: logger, analyzer: ticketAnalyzer}
	if ticketAnalyzer != nil {
		handler.structuredGenerator = ticketAnalyzer.GenerateStructured
	}
	if len(dispatchers) > 0 {
		handler.dispatcher = dispatchers[0]
	}
	return handler
}

// WithCallbacks configures the immutable outbound policy before serving requests.
func (h *TicketsHandler) WithCallbacks(callbacks *security.Callbacks) *TicketsHandler {
	h.callbacks = callbacks
	return h
}

func (h *TicketsHandler) WithBuilderConfig(loader settings.Loader) *TicketsHandler {
	h.builderConfig = loader
	return h
}

func (h *TicketsHandler) WithTenantDispatcher(dispatcher *channels.TenantDispatcher) *TicketsHandler {
	h.tenantDispatcher = dispatcher
	return h
}

func (h *TicketsHandler) analyzerBase(ctx context.Context) (*knowledge.Base, error) {
	return h.analyzer.TenantKnowledge(ctx)
}

func (h *TicketsHandler) Create(w http.ResponseWriter, r *http.Request) {
	// The router supplies tenant context for operator sessions; direct legacy
	// handler calls continue to default to the compatible default workspace.
	var request models.CreateTicketRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if security.IsEmployee(r.Context()) {
		p, _ := security.CurrentPrincipal(r.Context())
		request.UserEmail = p.Email
	}
	if len(request.RawText) > security.MaxTicketBytes || len(request.UserEmail) > 255 {
		writeError(w, 400, "ticket exceeds service limits")
		return
	}
	if strings.TrimSpace(request.RawText) == "" {
		writeError(w, http.StatusBadRequest, "raw_text is required")
		return
	}
	if request.Intake != nil {
		if err := intake.Validate(request.RawText, *request.Intake); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if !intake.Evaluate(request.RawText, *request.Intake).Ready {
			writeError(w, 400, "Заполните уточнения или выберите передачу без уточнений.")
			return
		}
		request.RawText = intake.Compose(request.RawText, *request.Intake)
		if len(request.RawText) > security.MaxTicketBytes {
			writeError(w, 400, "Обращение с уточнениями превышает допустимый объём.")
			return
		}
	}
	request.RawText = strings.ToValidUTF8(request.RawText, "�")
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	gate, gateErr := h.beginCreation(ctx, security.TenantID(ctx), "web:"+security.RequesterID(ctx), r.Header.Get("Idempotency-Key"), request)
	if gateErr != nil {
		writeCreationError(w, gateErr)
		return
	}
	defer gate.close(ctx)
	if gate.existing != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeJSON(w, http.StatusCreated, gate.existing)
		return
	}

	channels.LogIncoming(h.logger, "web", request.UserEmail)
	analysis := h.analyzer.Analyze(r.Context(), request.RawText)
	status := initialTicketStatus(analysis)

	const query = `INSERT INTO tickets (requester_id,tenant_id,user_email, channel, subject, callback_url, raw_text, department, priority, extracted_summary, rag_answer, pii_masked_count, pii_masked_types, status)
VALUES ($11,$1,$2, 'web', NULL, NULL, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, user_email, COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary, rag_answer, operator_reply, pii_masked_count, pii_masked_types, status, created_at, delivery_status`

	var ticket models.Ticket
	err := gate.query.QueryRow(r.Context(), query, security.TenantID(r.Context()), request.UserEmail, request.RawText, analysis.Department, analysis.Priority, analysis.ExtractedSummary, analysis.RAGAnswer, analysis.PIIMaskedCount, analysis.PIIMaskedTypes, status, security.RequesterID(r.Context())).Scan(
		&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department,
		&ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply,
		&ticket.PIIMaskedCount, &ticket.PIIMaskedTypes, &ticket.Status, &ticket.CreatedAt, &ticket.DeliveryStatus,
	)
	if err != nil {
		h.logger.Error("failed to create ticket")
		writeError(w, http.StatusInternalServerError, "failed to create ticket")
		return
	}
	if err := gate.finish(ctx, ticket.ID); err != nil {
		writeCreationError(w, err)
		return
	}
	h.auditPII(r.Context(), ticket.ID, ticket.PIIMaskedCount, ticket.PIIMaskedTypes)
	h.logger.Info(fmt.Sprintf("[TICKET #%d] status=%s | auto_replied=false | channel=Web | department=%s", ticket.ID, ticket.Status, ticket.Department))

	writeJSON(w, http.StatusCreated, ticket)
}

func (h *TicketsHandler) Ingest(w http.ResponseWriter, r *http.Request) {
	var request models.IngestRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "channel and message are required; channel must be web, email, bot or api")
		return
	}
	if err := h.validateIngest(request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if request.IdempotencyKey != "" && request.IdempotencyKey != key {
			writeError(w, 400, "conflicting idempotency keys")
			return
		}
		request.IdempotencyKey = key
	}
	result, err := h.IngestMessage(r.Context(), request)
	if err != nil {
		writeCreationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// IngestMessage is shared by the public Ingest endpoint and background channel workers.
func (h *TicketsHandler) IngestMessage(ctx context.Context, request models.IngestRequest) (channels.IngestResult, error) {
	botTenant := ""
	if request.Channel == "bot" && security.BotSenderChannel(request.Sender) != "" {
		botTenant = security.TenantID(ctx)
		var err error
		ctx, err = security.BotContext(ctx, h.db, request.Sender)
		if err != nil {
			return channels.IngestResult{}, err
		}
	}
	if request.Intake != nil {
		if err := intake.Validate(request.Message, *request.Intake); err != nil {
			return channels.IngestResult{}, err
		}
		if !intake.Evaluate(request.Message, *request.Intake).Ready {
			return channels.IngestResult{}, fmt.Errorf("incomplete intake")
		}
		request.Message = intake.Compose(request.Message, *request.Intake)
		request.Intake = nil
	}
	if err := h.validateIngest(request); err != nil {
		return channels.IngestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	namespace := security.TenantID(ctx)
	if botTenant != "" {
		namespace = botTenant
	}
	gate, err := h.beginCreation(ctx, namespace, "ingest:"+request.Channel+":"+request.Sender, request.IdempotencyKey, request)
	if err != nil {
		return channels.IngestResult{}, err
	}
	defer gate.close(ctx)
	if gate.existing != nil {
		return h.ingestResult(ctx, *gate.existing), nil
	}
	channels.LogIncoming(h.logger, request.Channel, request.Sender)
	rawText := strings.TrimSpace(strings.Join([]string{request.Subject, request.Message}, "\n"))
	analysis := h.analyzer.AnalyzeWithSender(ctx, rawText, request.Sender)
	status := initialTicketStatus(analysis)
	tenant := security.TenantID(ctx)
	const query = `INSERT INTO tickets (tenant_id,user_email, channel, subject, callback_url, raw_text, department, priority, extracted_summary, rag_answer, pii_masked_count, pii_masked_types, status,requester_id,bot_tenant_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,$14,$15)
	RETURNING id, user_email, COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary, rag_answer, operator_reply, pii_masked_count, pii_masked_types, status, created_at, delivery_status`
	var ticket models.Ticket
	err = gate.query.QueryRow(ctx, query, tenant, request.Sender, request.Channel, request.Subject, request.CallbackURL, rawText, analysis.Department, analysis.Priority, analysis.ExtractedSummary, analysis.RAGAnswer, analysis.PIIMaskedCount, analysis.PIIMaskedTypes, status, security.RequesterID(ctx), botTenant).Scan(
		&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department, &ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply, &ticket.PIIMaskedCount, &ticket.PIIMaskedTypes, &ticket.Status, &ticket.CreatedAt, &ticket.DeliveryStatus)
	if err != nil {
		return channels.IngestResult{}, err
	}
	if botTenant != "" {
		keys := append([]string{request.IdempotencyKey}, request.BotMessageKeys...)
		for _, key := range keys {
			if key == "" {
				continue
			}
			if !requestKeyPattern.MatchString(key) {
				return channels.IngestResult{}, errRequestKey
			}
			var receiptID int
			if err = gate.query.QueryRow(ctx, `INSERT INTO bot_dialogue_receipts(bot_tenant_id,sender,key_hash,ticket_id) VALUES($1,$2,$3,$4) ON CONFLICT(bot_tenant_id,sender,key_hash) DO UPDATE SET key_hash=EXCLUDED.key_hash RETURNING ticket_id`, botTenant, request.Sender, requestHash([]byte(key)), ticket.ID).Scan(&receiptID); err != nil {
				return channels.IngestResult{}, err
			}
			if receiptID != ticket.ID {
				return channels.IngestResult{}, errRequestConflict
			}
		}
	}
	if err := gate.finish(ctx, ticket.ID); err != nil {
		return channels.IngestResult{}, err
	}
	h.auditPII(ctx, ticket.ID, ticket.PIIMaskedCount, ticket.PIIMaskedTypes)
	h.logger.Info(fmt.Sprintf("[TICKET #%d] status=%s | auto_replied=%t | channel=%s | department=%s", ticket.ID, ticket.Status, ticket.RAGAnswer != nil, channels.DisplayChannel(ticket.Channel, request.Sender), ticket.Department))
	return h.ingestResult(ctx, ticket), nil
}

func (h *TicketsHandler) ingestResult(ctx context.Context, ticket models.Ticket) channels.IngestResult {
	builderConfig := settings.LoadOrDefault(ctx, h.builderConfig)
	replyDepartment := ticket.Department
	if label := departmentLabel(replyDepartment); label != "" {
		replyDepartment = label
	}
	replyText := channels.BuilderEscalationReplyText(ticket.ID, replyDepartment, builderConfig.HandoffTemplate)
	autoReplied := ticket.RAGAnswer != nil
	if autoReplied {
		replyText = channels.PublicReplyText(ticket.ID, channels.AppendSignature(*ticket.RAGAnswer, builderConfig.Signature))
	}
	if ticket.OperatorReply != nil && strings.TrimSpace(*ticket.OperatorReply) != "" {
		replyText = channels.OperatorReplyText(ticket.ID, *ticket.OperatorReply)
		autoReplied = false
	}
	return channels.IngestResult{TicketID: ticket.ID, Channel: ticket.Channel, AutoReplied: autoReplied, ReplyText: replyText, Department: ticket.Department, Priority: ticket.Priority, Status: ticket.Status, ExtractedSummary: ticket.ExtractedSummary, PIIMaskedCount: ticket.PIIMaskedCount, PIIMaskedTypes: ticket.PIIMaskedTypes}
}

// User-controlled channel/confirmation flags cannot authorize ticket closure.
func initialTicketStatus(analysis analyzer.Result) string {
	if analysis.RAGAnswer != nil {
		return "awaiting_confirmation"
	}
	return "assigned_to_operator"
}

func validChannel(value string) bool { return hasValue(value, "web", "email", "bot", "api", "widget") }
func departmentLabel(value string) string {
	if value == "Billing" {
		return "Финансы и биллинг"
	}
	if label, ok := map[string]string{"IT": "IT", "HR": "HR", "Accounting": "Бухгалтерию", "Facilities": "АХО и офис", "General": "Общий отдел"}[value]; ok {
		return label
	}
	return value
}

func (h *TicketsHandler) Feedback(w http.ResponseWriter, r *http.Request) {
	if _, session := security.CurrentPrincipal(r.Context()); !session && !security.IsOperatorAuthenticated(r.Context()) && r.Header.Get("Origin") == "" {
		writeError(w, http.StatusUnauthorized, "integration authorization required")
		return
	}
	id, ok := ticketID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	var request models.FeedbackRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	status := "assigned_to_operator"
	if request.Resolved {
		status = "auto_resolved"
	}
	tx, err := h.beginTicketTx(r.Context())
	if err != nil {
		writeError(w, 500, "failed to record feedback")
		return
	}
	defer tx.Rollback(r.Context())
	var current string
	var rag, operatorReply *string
	err = tx.QueryRow(r.Context(), `SELECT status,rag_answer,operator_reply FROM tickets WHERE id=$1 AND tenant_id=$2 AND ($3=FALSE OR requester_id=$4) FOR UPDATE`, id, security.TenantID(r.Context()), security.IsEmployee(r.Context()), security.RequesterID(r.Context())).Scan(&current, &rag, &operatorReply)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "ticket not found")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to read ticket feedback")
		return
	}
	eligibleAnswer := rag != nil && strings.TrimSpace(*rag) != "" && (operatorReply == nil || strings.TrimSpace(*operatorReply) == "")
	if current != "awaiting_confirmation" || !eligibleAnswer {
		// A repeated click is safe only when this exact decision was already audited.
		var previous bool
		if current == status && eligibleAnswer {
			err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE tenant_id=$1 AND ticket_id=$2 AND event_type='TICKET_FEEDBACK' AND details=$3)`, security.TenantID(r.Context()), id, "status="+status).Scan(&previous)
			if err != nil {
				writeError(w, 500, "failed to read feedback audit")
				return
			}
		}
		if !previous {
			writeError(w, 409, "Ответ уже обработан или обращение не ожидает подтверждения регламента")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": status})
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE tickets SET status=$1 WHERE id=$2 AND tenant_id=$3`, status, id, security.TenantID(r.Context())); err != nil {
		writeError(w, 500, "failed to update ticket feedback")
		return
	}
	actor := security.RequesterID(r.Context())
	if actor == "" {
		actor = "integration"
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,ticket_id,details) VALUES($1,'TICKET_FEEDBACK',$2,$3,$4)`, security.TenantID(r.Context()), actor, id, "status="+status); err != nil {
		writeError(w, 500, "failed to record feedback audit")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit feedback")
		return
	}
	h.logger.Info("ticket feedback received", "id", id, "resolved", request.Resolved, "status", status)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": status})
}

func (h *TicketsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	var request models.UpdateTicketRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var reason string
	if request.ClosureReason != nil {
		reason = strings.TrimSpace(*request.ClosureReason)
		if request.Status == nil || *request.Status != "closed" || !h.validClosureReason(r.Context(), reason) || request.OperatorReply != nil {
			writeError(w, 400, "invalid closure reason")
			return
		}
	}
	fields := make([]string, 0, 3)
	args := make([]any, 0, 5)
	if request.Status != nil {
		if !validStatus(*request.Status) {
			writeError(w, http.StatusBadRequest, "invalid status")
			return
		}
		fields = append(fields, "status = $"+strconv.Itoa(len(args)+1))
		args = append(args, *request.Status)
	}
	if request.Department != nil {
		if !validDepartment(*request.Department) {
			writeError(w, http.StatusBadRequest, "invalid department")
			return
		}
		fields = append(fields, "department = $"+strconv.Itoa(len(args)+1))
		args = append(args, *request.Department)
	}
	if request.Priority != nil {
		if !validPriority(*request.Priority) {
			writeError(w, http.StatusBadRequest, "invalid priority")
			return
		}
		fields = append(fields, "priority = $"+strconv.Itoa(len(args)+1))
		args = append(args, *request.Priority)
	}
	if request.OperatorReply != nil {
		fields = append(fields, "operator_reply = $"+strconv.Itoa(len(args)+1))
		args = append(args, strings.TrimSpace(*request.OperatorReply))
	}

	if len(fields) == 0 {
		writeError(w, http.StatusBadRequest, "at least one field is required")
		return
	}
	args = append(args, id, security.TenantID(r.Context()))
	query := "UPDATE tickets SET " + strings.Join(fields, ", ") + " WHERE id = $" + strconv.Itoa(len(args)-1) + " AND tenant_id=$" + strconv.Itoa(len(args))
	if request.OperatorReply != nil && (strings.TrimSpace(*request.OperatorReply) == "" || len(*request.OperatorReply) > security.MaxDocumentBytes) {
		writeError(w, 400, "operator reply exceeds limits or is empty")
		return
	}
	if err := h.applyUpdate(r.Context(), id, query, args, request.OperatorReply, reason); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "ticket not found")
		} else {
			writeError(w, 500, "ticket update failed")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true})
}

func (h *TicketsHandler) Reply(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	var request struct {
		Reply string `json:"operator_reply"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil || strings.TrimSpace(request.Reply) == "" {
		writeError(w, http.StatusBadRequest, "operator_reply is required")
		return
	}
	if len(request.Reply) > security.MaxDocumentBytes {
		writeError(w, 400, "operator reply exceeds limits")
		return
	}
	query := "UPDATE tickets SET operator_reply=$1 WHERE id=$2 AND tenant_id=$3"
	if err := h.applyUpdate(r.Context(), id, query, []any{strings.TrimSpace(request.Reply), id, security.TenantID(r.Context())}, &request.Reply); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "ticket not found")
		} else {
			writeError(w, 500, "ticket reply failed")
		}
		return
	}
	h.Detail(w, r)
}

func (h *TicketsHandler) applyUpdate(ctx context.Context, id int, query string, args []any, reply *string, closureReasons ...string) error {
	tx, err := h.beginTicketTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Lock first to serialize reply revisions with queue acknowledgements.
	var channel, sender, callback, oldDepartment string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(channel,'web'),COALESCE(user_email,''),COALESCE(callback_url,''),COALESCE(department,'') FROM tickets WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, id, security.TenantID(ctx)).Scan(&channel, &sender, &callback, &oldDepartment); err != nil {
		return err
	}
	if reply != nil {
		if err = h.callbacks.Validate(callback); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, query, args...); err != nil {
		return err
	}
	if reply != nil {
		if _, err = tx.Exec(ctx, `UPDATE tickets SET status='resolved' WHERE id=$1 AND tenant_id=$2`, id, security.TenantID(ctx)); err != nil {
			return err
		}
		if err = delivery.EnqueueReply(ctx, tx, id, channel, sender, callback, strings.TrimSpace(*reply)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,ticket_id,details) VALUES($1,'OPERATOR_REPLY','operator',$2,'Reply saved with durable delivery events')`, security.TenantID(ctx), id); err != nil {
			return err
		}
	}
	var newDepartment, newStatus string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(department,''),status FROM tickets WHERE tenant_id=$1 AND id=$2`, security.TenantID(ctx), id).Scan(&newDepartment, &newStatus); err != nil {
		return err
	}
	if newDepartment != oldDepartment {
		actor := security.RequesterID(ctx)
		if actor == "" {
			actor = "operator"
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,ticket_id,details) VALUES($1,'TICKET_ROUTING_CORRECTED',$2,$3,$4)`, security.TenantID(ctx), actor, id, "from="+oldDepartment+";to="+newDepartment); err != nil {
			return err
		}
	}
	if newStatus == "closed" || newStatus == "resolved" || newStatus == "auto_resolved" {
		if _, err = tx.Exec(ctx, `UPDATE ticket_work_sessions SET active_seconds=active_seconds+LEAST(30,GREATEST(0,EXTRACT(EPOCH FROM NOW()-last_seen_at))),running=FALSE,last_seen_at=NOW() WHERE tenant_id=$1 AND ticket_id=$2 AND running=TRUE`, security.TenantID(ctx), id); err != nil {
			return err
		}
	}
	if newStatus == "closed" && len(closureReasons) > 0 && closureReasons[0] != "" {
		if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,ticket_id,details) VALUES($1,'TICKET_CLOSED',$2,$3,$4)`, security.TenantID(ctx), security.RequesterID(ctx), id, closureReasons[0]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Deliver is called by the bounded durable worker, never by request goroutines.
func (h *TicketsHandler) Deliver(ctx context.Context, m delivery.Message) error {
	switch m.Kind {
	case "callback":
		body, _ := json.Marshal(map[string]any{"ticket_id": m.TicketID, "operator_reply": m.Body})
		err := h.callbacks.Post(ctx, m.Recipient, bytes.NewReader(body))
		if err != nil {
			h.logger.Error(fmt.Sprintf("[OUTGOING] Failed to send reply via callback (ticket=#%d): %s", m.TicketID, security.SafeLogError(err)))
		} else {
			h.logger.Info(fmt.Sprintf("[OUTGOING] Reply sent via callback (ticket=#%d, source=Operator)", m.TicketID))
		}
		return err
	case "channel":
		if h.dispatcher == nil && h.tenantDispatcher == nil {
			return fmt.Errorf("channel dispatcher unavailable")
		}
		var originalMessage string
		var originalSubject string
		if m.Channel == "email" {
			if err := h.db.QueryRow(ctx, `SELECT raw_text,COALESCE(subject,'') FROM tickets WHERE id=$1 AND tenant_id=$2`, m.TicketID, m.TenantID).Scan(&originalMessage, &originalSubject); err != nil {
				return fmt.Errorf("load original ticket message: %w", err)
			}
		}
		routeTenant := m.TenantID
		if m.Channel == "bot" {
			var originalBotTenant, requester string
			if loadErr := h.db.QueryRow(ctx, `SELECT bot_tenant_id,requester_id FROM tickets WHERE tenant_id=$1 AND id=$2`, m.TenantID, m.TicketID).Scan(&originalBotTenant, &requester); loadErr != nil {
				return loadErr
			}
			if originalBotTenant != "" {
				routeTenant = originalBotTenant
			}
			if m.BotRouteTenant != "" {
				routeTenant = m.BotRouteTenant
			}
			// Recheck at delivery time: an old queued answer must never follow an
			// unlinked, disabled or reassigned chat to another website account.
			if requester != "" {
				linked, linkErr := security.BotContext(security.WithTenantID(ctx, routeTenant), h.db, m.Recipient)
				if linkErr != nil || security.RequesterID(linked) != requester || security.TenantID(linked) != m.TenantID {
					return fmt.Errorf("bot delivery blocked: account binding is no longer valid")
				}
			}
		}
		var err error
		if h.tenantDispatcher != nil {
			err = h.tenantDispatcher.Dispatch(ctx, routeTenant, m.Channel, m.Recipient, m.TicketID, originalSubject, originalMessage, m.Body)
		} else {
			err = h.dispatcher.Dispatch(ctx, m.Channel, m.Recipient, m.TicketID, originalSubject, originalMessage, m.Body)
		}
		channels.LogOutgoing(h.logger, m.Channel, m.Recipient, m.TicketID, "Operator", err)
		return err
	default:
		return fmt.Errorf("unknown delivery kind")
	}
}

func (h *TicketsHandler) sendWebhook(callbackURL string, ticketID int, reply string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"ticket_id": ticketID, "operator_reply": reply})
	if err := h.callbacks.Post(ctx, callbackURL, bytes.NewReader(body)); err != nil {
		h.logger.Error("webhook delivery failed", "ticket_id", ticketID)
	}
}

// Enforce limits for HTTP adapters AND background channel workers before analysis/storage.
func (h *TicketsHandler) validateIngest(r models.IngestRequest) error {
	if r.IdempotencyKey != "" && !requestKeyPattern.MatchString(r.IdempotencyKey) {
		return errRequestKey
	}
	if r.Channel == "bot" && strings.HasPrefix(r.Sender, "max_chat:") {
		return fmt.Errorf("corporate support requests require a private chat")
	}
	if !validChannel(r.Channel) || strings.TrimSpace(r.Message) == "" {
		return fmt.Errorf("channel and message are required")
	}
	if len(r.Subject)+len(r.Message)+1 > security.MaxTicketBytes || len(r.Sender) > 255 {
		return fmt.Errorf("ticket exceeds service limits")
	}
	if err := h.callbacks.Validate(r.CallbackURL); err != nil {
		return err
	}
	return nil
}

func (h *TicketsHandler) Audit(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `SELECT id, created_at, event_type, actor, ticket_id, COALESCE(details, '') FROM audit_logs WHERE tenant_id=$1 ORDER BY created_at DESC, id DESC LIMIT 30`, security.TenantID(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load audit log")
		return
	}
	defer rows.Close()
	type auditEntry struct {
		ID        int       `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		EventType string    `json:"event_type"`
		Actor     string    `json:"actor"`
		TicketID  *int      `json:"ticket_id"`
		Details   string    `json:"details"`
	}
	entries := make([]auditEntry, 0, 30)
	for rows.Next() {
		var entry auditEntry
		if err := rows.Scan(&entry.ID, &entry.CreatedAt, &entry.EventType, &entry.Actor, &entry.TicketID, &entry.Details); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read audit log")
			return
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read audit log")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *TicketsHandler) auditPII(ctx context.Context, ticketID, count int, types []string) {
	if count <= 0 {
		return
	}
	details := fmt.Sprintf("Обезличено объектов: %d; типы: %s", count, strings.Join(types, ", "))
	if _, err := h.db.Exec(ctx, `INSERT INTO audit_logs (tenant_id,event_type, actor, ticket_id, details) VALUES ($1,'PII_MASKED', 'system-pii-filter', $2, $3)`, security.TenantID(ctx), ticketID, details); err != nil {
		h.logger.Error("failed to record PII audit event", "ticket_id", ticketID)
	}
}

func ticketID(r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	return id, err == nil && id > 0
}
func validStatus(value string) bool {
	return hasValue(value, "new", "awaiting_confirmation", "assigned_to_operator", "auto_resolved", "resolved", "closed")
}
func validDepartment(value string) bool { return knowledge.ValidDepartmentName(value) }

func validPriority(value string) bool { return hasValue(value, "low", "medium", "high") }
func hasValue(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func (h *TicketsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, before := 100, 0
	for key, target := range map[string]*int{"limit": &limit, "before_id": &before} {
		if values, present := r.URL.Query()[key]; present {
			if len(values) != 1 {
				writeError(w, 400, "pagination parameter must occur once")
				return
			}
			parsed, err := strconv.ParseInt(values[0], 10, 32)
			if err != nil || parsed < 0 || (key == "limit" && (parsed < 1 || parsed > 200)) {
				writeError(w, 400, "invalid pagination parameters: limit 1..200, before_id nonnegative")
				return
			}
			*target = int(parsed)
		}
	}

	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(search)) > 500 {
		writeError(w, 400, "Поисковый запрос слишком длинный")
		return
	}
	idText := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(search), "id"))
	idText = strings.TrimSpace(strings.TrimLeft(idText, "#№: "))
	searchID, _ := strconv.ParseInt(idText, 10, 32)
	if searchID < 0 {
		searchID = 0
	}
	pattern := "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(strings.ReplaceAll(strings.ToLower(search), "ё", "е")) + "%"

	queueStatus := r.URL.Query().Get("queue_status")
	if queueStatus != "" && !hasValue(queueStatus, "new", "in_progress", "closed") {
		writeError(w, 400, "invalid queue status")
		return
	}
	const query = `
SELECT id, user_email, COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary,
       rag_answer, operator_reply, pii_masked_count, pii_masked_types, status, created_at, delivery_status,
       ($7::boolean AND status NOT IN ('closed','auto_resolved') AND (
         NOT EXISTS(SELECT 1 FROM ticket_operator_reads r WHERE r.tenant_id=t.tenant_id AND r.ticket_id=t.id AND r.operator_id=$6)
         OR EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND m.role='employee' AND m.id>COALESCE((SELECT r.last_message_id FROM ticket_operator_reads r WHERE r.tenant_id=t.tenant_id AND r.ticket_id=t.id AND r.operator_id=$6),0))
       ))
, ` + ticketDisplayStatusSQL + `, ` + ticketRequesterEmailSQL + `
FROM tickets t WHERE tenant_id=$1 AND flowops_ticket_visible(t.tenant_id,$3,t.department,t.requester_id) AND ($2=FALSE OR requester_id=$3) AND ($4=0 OR id<$4)
AND ($8<>'' OR NOT $7 OR NOT EXISTS(SELECT 1 FROM ticket_queue_hidden h WHERE h.tenant_id=t.tenant_id AND h.ticket_id=t.id AND h.operator_id=$6 AND NOT EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND m.role='employee' AND m.source_key<>'initial' AND m.id>h.last_employee_message_id)))
AND ($8='' OR ($10::bigint>0 AND t.id=$10) OR ($10::bigint=0 AND (translate(concat_ws(E'\n',user_email,subject,raw_text,extracted_summary,operator_reply,` + ticketRequesterEmailSQL + `),'АБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯё','абвгдеежзийклмнопрстуфхцчшщъыьэюяе') ILIKE $9 OR EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND translate(m.body,'АБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯё','абвгдеежзийклмнопрстуфхцчшщъыьэюяе') ILIKE $9))))
AND ($11='' OR (` + ticketDisplayStatusSQL + `)=$11)
ORDER BY id DESC LIMIT $5`

	reader := webDialogueActor(r.Context())
	rows, err := h.db.Query(r.Context(), query, security.TenantID(r.Context()), security.IsEmployee(r.Context()), security.RequesterID(r.Context()), before, limit+1, reader.id, reader.operator, search, pattern, searchID, queueStatus)
	if err != nil {
		h.logger.Error("failed to list tickets")
		writeError(w, http.StatusInternalServerError, "failed to list tickets")
		return
	}
	defer rows.Close()

	tickets := make([]models.Ticket, 0)
	for rows.Next() {
		var ticket models.Ticket
		if err := rows.Scan(
			&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department,
			&ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply, &ticket.PIIMaskedCount, &ticket.PIIMaskedTypes,
			&ticket.Status, &ticket.CreatedAt, &ticket.DeliveryStatus, &ticket.UnreadEmployeeMessage, &ticket.DisplayStatus, &ticket.RequesterEmail,
		); err != nil {
			h.logger.Error("failed to scan ticket")
			writeError(w, http.StatusInternalServerError, "failed to read tickets")
			return
		}
		tickets = append(tickets, ticket)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("failed while reading tickets")
		writeError(w, http.StatusInternalServerError, "failed to read tickets")
		return
	}

	if len(tickets) > limit {
		tickets = tickets[:limit]
		w.Header().Set("X-Next-Cursor", strconv.Itoa(tickets[len(tickets)-1].ID))
	}
	writeJSON(w, http.StatusOK, tickets)
}

func (h *TicketsHandler) Detail(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	const query = `SELECT id, COALESCE(user_email,''), COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary,
       rag_answer, operator_reply, pii_masked_count, pii_masked_types, status, created_at, delivery_status,
COALESCE((SELECT a.details FROM audit_logs a WHERE a.tenant_id=t.tenant_id AND a.ticket_id=t.id AND a.event_type='TICKET_CLOSED' AND a.created_at>=t.created_at ORDER BY a.created_at DESC,a.id DESC LIMIT 1),''), ` + ticketDisplayStatusSQL + `, ` + ticketRequesterEmailSQL + `, first_response_due_at,resolution_due_at
FROM tickets t WHERE id = $1 AND tenant_id=$2 AND ($3=FALSE OR requester_id=$4)`
	var ticket models.Ticket
	err := h.db.QueryRow(r.Context(), query, id, security.TenantID(r.Context()), security.IsEmployee(r.Context()), security.RequesterID(r.Context())).Scan(
		&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department,
		&ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply, &ticket.PIIMaskedCount, &ticket.PIIMaskedTypes, &ticket.Status, &ticket.CreatedAt, &ticket.DeliveryStatus, &ticket.ClosureReason, &ticket.DisplayStatus, &ticket.RequesterEmail, &ticket.FirstResponseDueAt, &ticket.ResolutionDueAt,
	)
	if err != nil {
		writeError(w, http.StatusNotFound, "ticket not found")
		return
	}
	writeJSON(w, http.StatusOK, ticket)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
