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
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TicketsHandler struct {
	db               *pgxpool.Pool
	logger           *slog.Logger
	analyzer         *analyzer.Analyzer
	dispatcher       *channels.Dispatcher
	tenantDispatcher *channels.TenantDispatcher
	builderConfig    settings.Loader
	callbacks        *security.Callbacks
}

func NewTicketsHandler(db *pgxpool.Pool, logger *slog.Logger, ticketAnalyzer *analyzer.Analyzer, dispatchers ...*channels.Dispatcher) *TicketsHandler {
	handler := &TicketsHandler{db: db, logger: logger, analyzer: ticketAnalyzer}
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
	if strings.TrimSpace(request.RawText) == "" {
		writeError(w, http.StatusBadRequest, "raw_text is required")
		return
	}
	request.RawText = strings.ToValidUTF8(request.RawText, "�")
	channels.LogIncoming(h.logger, "web", request.UserEmail)
	analysis := h.analyzer.Analyze(r.Context(), request.RawText)
	status := initialTicketStatus(analysis)

	const query = `INSERT INTO tickets (tenant_id,user_email, channel, subject, callback_url, raw_text, department, priority, extracted_summary, rag_answer, pii_masked_count, pii_masked_types, status)
VALUES ($1,$2, 'web', NULL, NULL, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, user_email, COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary, rag_answer, operator_reply, pii_masked_count, pii_masked_types, status, created_at, delivery_status`

	var ticket models.Ticket
	err := h.db.QueryRow(r.Context(), query, security.TenantID(r.Context()), request.UserEmail, request.RawText, analysis.Department, analysis.Priority, analysis.ExtractedSummary, analysis.RAGAnswer, analysis.PIIMaskedCount, analysis.PIIMaskedTypes, status).Scan(
		&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department,
		&ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply,
		&ticket.PIIMaskedCount, &ticket.PIIMaskedTypes, &ticket.Status, &ticket.CreatedAt, &ticket.DeliveryStatus,
	)
	if err != nil {
		h.logger.Error("failed to create ticket")
		writeError(w, http.StatusInternalServerError, "failed to create ticket")
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
	result, err := h.IngestMessage(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to ingest ticket")
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// IngestMessage is shared by the public Ingest endpoint and background channel workers.
func (h *TicketsHandler) IngestMessage(ctx context.Context, request models.IngestRequest) (channels.IngestResult, error) {
	if err := h.validateIngest(request); err != nil {
		return channels.IngestResult{}, err
	}
	channels.LogIncoming(h.logger, request.Channel, request.Sender)
	rawText := strings.TrimSpace(strings.Join([]string{request.Subject, request.Message}, "\n"))
	analysis := h.analyzer.AnalyzeWithSender(ctx, rawText, request.Sender)
	status := initialTicketStatus(analysis)
	autoReplied := analysis.RAGAnswer != nil
	builderConfig := settings.LoadOrDefault(ctx, h.builderConfig)
	tenant := security.TenantID(ctx)
	const query = `INSERT INTO tickets (tenant_id,user_email, channel, subject, callback_url, raw_text, department, priority, extracted_summary, rag_answer, pii_masked_count, pii_masked_types, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	RETURNING id, user_email, COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary, rag_answer, operator_reply, pii_masked_count, pii_masked_types, status, created_at, delivery_status`
	var ticket models.Ticket
	err := h.db.QueryRow(ctx, query, tenant, request.Sender, request.Channel, request.Subject, request.CallbackURL, rawText, analysis.Department, analysis.Priority, analysis.ExtractedSummary, analysis.RAGAnswer, analysis.PIIMaskedCount, analysis.PIIMaskedTypes, status).Scan(
		&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department, &ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply, &ticket.PIIMaskedCount, &ticket.PIIMaskedTypes, &ticket.Status, &ticket.CreatedAt, &ticket.DeliveryStatus)
	if err != nil {
		return channels.IngestResult{}, err
	}
	replyDepartment := analysis.Department
	if replyDepartment != "" && replyDepartment != "Unassigned" && replyDepartment != "General" {
		if label := departmentLabel(replyDepartment); label != "" {
			replyDepartment = label
		}
	}
	replyText := channels.BuilderEscalationReplyText(ticket.ID, replyDepartment, builderConfig.HandoffTemplate)
	if autoReplied {
		replyText = channels.PublicReplyText(ticket.ID, channels.AppendSignature(*analysis.RAGAnswer, builderConfig.Signature))
	}
	h.auditPII(ctx, ticket.ID, ticket.PIIMaskedCount, ticket.PIIMaskedTypes)
	h.logger.Info(fmt.Sprintf("[TICKET #%d] status=%s | auto_replied=%t | channel=%s | department=%s", ticket.ID, ticket.Status, autoReplied, channels.DisplayChannel(ticket.Channel, request.Sender), ticket.Department))
	return channels.IngestResult{TicketID: ticket.ID, Channel: ticket.Channel, AutoReplied: autoReplied, ReplyText: replyText, Department: ticket.Department, Priority: ticket.Priority, Status: ticket.Status, ExtractedSummary: ticket.ExtractedSummary, PIIMaskedCount: ticket.PIIMaskedCount, PIIMaskedTypes: ticket.PIIMaskedTypes}, nil
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
	if !security.IsOperatorAuthenticated(r.Context()) && r.Header.Get("Origin") == "" {
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
	h.logger.Info("ticket feedback received", "id", id, "resolved", request.Resolved, "status", status)
	if tag, err := h.db.Exec(r.Context(), `UPDATE tickets SET status = $1 WHERE id = $2 AND tenant_id=$3`, status, id, security.TenantID(r.Context())); err != nil || tag.RowsAffected() == 0 {
		if err != nil {
			h.logger.Error("failed to update ticket feedback")
		}
		writeError(w, http.StatusNotFound, "ticket not found")
		return
	}
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
	if err := h.applyUpdate(r.Context(), id, query, args, request.OperatorReply); err != nil {
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

func (h *TicketsHandler) applyUpdate(ctx context.Context, id int, query string, args []any, reply *string) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Lock first to serialize reply revisions with queue acknowledgements.
	var channel, sender, callback string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(channel,'web'),COALESCE(user_email,''),COALESCE(callback_url,'') FROM tickets WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, id, security.TenantID(ctx)).Scan(&channel, &sender, &callback); err != nil {
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
		var err error
		if h.tenantDispatcher != nil {
			err = h.tenantDispatcher.Dispatch(ctx, m.TenantID, m.Channel, m.Recipient, m.TicketID, originalSubject, originalMessage, m.Body)
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

func (h *TicketsHandler) Reset(w http.ResponseWriter, r *http.Request) {
	if _, err := h.db.Exec(r.Context(), `DELETE FROM ticket_deliveries WHERE ticket_id IN (SELECT id FROM tickets WHERE tenant_id=$1)`, security.TenantID(r.Context())); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reset tickets")
		return
	}
	if _, err := h.db.Exec(r.Context(), `DELETE FROM tickets WHERE tenant_id=$1`, security.TenantID(r.Context())); err != nil {
		h.logger.Error("failed to reset tickets", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reset tickets")
		return
	}
	base, err := h.analyzerBase(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reset knowledge base")
		return
	}
	if err := base.Reset(); err != nil {
		h.logger.Error("failed to reset knowledge base", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reset knowledge base")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reset": true})
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
func validDepartment(value string) bool {
	return hasValue(knowledge.NormalizeDepartment(value), "IT", "Billing", "HR", "Accounting", "Facilities", "General")
}
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
	const query = `
SELECT id, user_email, COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary,
       rag_answer, operator_reply, pii_masked_count, pii_masked_types, status, created_at, delivery_status
FROM tickets WHERE tenant_id=$1
ORDER BY created_at DESC, id DESC`

	rows, err := h.db.Query(r.Context(), query, security.TenantID(r.Context()))
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
			&ticket.Status, &ticket.CreatedAt, &ticket.DeliveryStatus,
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

	writeJSON(w, http.StatusOK, tickets)
}

func (h *TicketsHandler) Detail(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	const query = `SELECT id, user_email, COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary,
       rag_answer, operator_reply, pii_masked_count, pii_masked_types, status, created_at, delivery_status
FROM tickets WHERE id = $1 AND tenant_id=$2`
	var ticket models.Ticket
	err := h.db.QueryRow(r.Context(), query, id, security.TenantID(r.Context())).Scan(
		&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department,
		&ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply, &ticket.PIIMaskedCount, &ticket.PIIMaskedTypes, &ticket.Status, &ticket.CreatedAt, &ticket.DeliveryStatus,
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
