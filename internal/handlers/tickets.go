package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TicketsHandler struct {
	db       *pgxpool.Pool
	logger   *slog.Logger
	analyzer *analyzer.Analyzer
}

func NewTicketsHandler(db *pgxpool.Pool, logger *slog.Logger, ticketAnalyzer *analyzer.Analyzer) *TicketsHandler {
	return &TicketsHandler{db: db, logger: logger, analyzer: ticketAnalyzer}
}

func (h *TicketsHandler) Create(w http.ResponseWriter, r *http.Request) {
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
	h.logger.Info("new ticket received", "user_email", request.UserEmail, "raw_text", request.RawText)
	analysis := h.analyzer.Analyze(r.Context(), request.RawText)
	status := "assigned_to_operator"
	if analysis.RAGAnswer != nil {
		status = "awaiting_confirmation"
	}

	const query = `INSERT INTO tickets (user_email, channel, subject, callback_url, raw_text, department, priority, extracted_summary, rag_answer, status)
VALUES ($1, 'web', NULL, NULL, $2, $3, $4, $5, $6, $7)
RETURNING id, user_email, COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary, rag_answer, operator_reply, status, created_at`

	var ticket models.Ticket
	err := h.db.QueryRow(r.Context(), query, request.UserEmail, request.RawText, analysis.Department, analysis.Priority, analysis.ExtractedSummary, analysis.RAGAnswer, status).Scan(
		&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department,
		&ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply,
		&ticket.Status, &ticket.CreatedAt,
	)
	if err != nil {
		h.logger.Error("failed to create ticket", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create ticket")
		return
	}
	h.logger.Info("ticket saved", "id", ticket.ID, "status", ticket.Status, "department", ticket.Department)

	writeJSON(w, http.StatusCreated, ticket)
}

func (h *TicketsHandler) Ingest(w http.ResponseWriter, r *http.Request) {
	var request models.IngestRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || !validChannel(request.Channel) || strings.TrimSpace(request.Message) == "" {
		writeError(w, http.StatusBadRequest, "channel and message are required; channel must be web, email, bot or api")
		return
	}
	rawText := strings.TrimSpace(strings.Join([]string{request.Subject, request.Message}, "\n"))
	analysis := h.analyzer.Analyze(r.Context(), rawText)
	status := "assigned_to_operator"
	autoReplied := false
	replyText := fmt.Sprintf("Ваше обращение принято и направлено в профильный отдел: %s.", departmentLabel(analysis.Department))
	if analysis.RAGAnswer != nil {
		autoReplied = true
		replyText = strings.TrimSpace(*analysis.RAGAnswer)
		status = "auto_resolved"
		if request.RequireConfirmation {
			status = "awaiting_confirmation"
		}
	}
	const query = `INSERT INTO tickets (user_email, channel, subject, callback_url, raw_text, department, priority, extracted_summary, rag_answer, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	RETURNING id, user_email, COALESCE(channel, 'web'), COALESCE(subject, ''), COALESCE(callback_url, ''), raw_text, department, priority, extracted_summary, rag_answer, operator_reply, status, created_at`
	var ticket models.Ticket
	err := h.db.QueryRow(r.Context(), query, request.Sender, request.Channel, request.Subject, request.CallbackURL, rawText, analysis.Department, analysis.Priority, analysis.ExtractedSummary, analysis.RAGAnswer, status).Scan(
		&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department, &ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply, &ticket.Status, &ticket.CreatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to ingest ticket")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ticket_id": ticket.ID, "channel": ticket.Channel, "auto_replied": autoReplied, "reply_text": replyText, "department": ticket.Department, "priority": ticket.Priority, "status": ticket.Status, "extracted_summary": ticket.ExtractedSummary})
}

func validChannel(value string) bool { return hasValue(value, "web", "email", "bot", "api") }
func departmentLabel(value string) string {
	if value == "Billing" {
		return "Финансы и биллинг"
	}
	return map[string]string{"IT": "IT", "HR": "HR", "Accounting": "Бухгалтерию", "Facilities": "АХО и офис", "General": "Общий отдел"}[value]
}

func (h *TicketsHandler) Feedback(w http.ResponseWriter, r *http.Request) {
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
	if tag, err := h.db.Exec(r.Context(), `UPDATE tickets SET status = $1 WHERE id = $2`, status, id); err != nil || tag.RowsAffected() == 0 {
		if err != nil {
			h.logger.Error("failed to update ticket feedback", "error", err)
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
	var callbackURL string
	if request.OperatorReply != nil {
		_ = h.db.QueryRow(r.Context(), `SELECT callback_url FROM tickets WHERE id = $1`, id).Scan(&callbackURL)
	}
	if len(fields) == 0 {
		writeError(w, http.StatusBadRequest, "at least one field is required")
		return
	}
	args = append(args, id)
	query := "UPDATE tickets SET " + strings.Join(fields, ", ") + " WHERE id = $" + strconv.Itoa(len(args))
	if tag, err := h.db.Exec(r.Context(), query, args...); err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "ticket not found")
		return
	}
	if request.OperatorReply != nil {
		h.logger.Info("operator reply saved", "id", id, "status", request.Status, "reply_length", len(*request.OperatorReply))
		if callbackURL != "" {
			go h.sendWebhook(callbackURL, id, strings.TrimSpace(*request.OperatorReply))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true})
}

func (h *TicketsHandler) sendWebhook(callbackURL string, ticketID int, reply string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"ticket_id": ticketID, "operator_reply": reply})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		h.logger.Error("webhook callback failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		h.logger.Error("webhook callback failed", "error", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		h.logger.Info("webhook callback sent", "ticket_id", ticketID, "status", resp.StatusCode)
	} else {
		h.logger.Error("webhook callback failed", "ticket_id", ticketID, "status", resp.StatusCode)
	}
}

func (h *TicketsHandler) Reset(w http.ResponseWriter, r *http.Request) {
	if _, err := h.db.Exec(r.Context(), `TRUNCATE TABLE tickets RESTART IDENTITY`); err != nil {
		h.logger.Error("failed to reset tickets", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reset tickets")
		return
	}
	if err := h.analyzer.ResetKnowledge(); err != nil {
		h.logger.Error("failed to reset knowledge base", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reset knowledge base")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reset": true})
}

func (h *TicketsHandler) Stats(w http.ResponseWriter, r *http.Request) {
	stats := struct {
		Total        int            `json:"total"`
		AutoResolved int            `json:"auto_resolved"`
		Assigned     int            `json:"assigned_to_operator"`
		Closed       int            `json:"closed"`
		Deflection   float64        `json:"deflection_rate_percent"`
		ByDepartment map[string]int `json:"by_department"`
		ByChannel    map[string]int `json:"by_channel"`
		AIMode       string         `json:"ai_mode"`
		AIModel      string         `json:"ai_model"`
	}{ByDepartment: make(map[string]int), ByChannel: make(map[string]int), AIMode: "heuristic_fallback", AIModel: os.Getenv("AI_MODEL")}
	if os.Getenv("AI_API_KEY") != "" || os.Getenv("GEMINI_API_KEY") != "" {
		stats.AIMode = "llm"
	}
	if stats.AIModel == "" {
		stats.AIModel = "gemini-3.8-flash"
	}
	if err := h.db.QueryRow(r.Context(), `SELECT COUNT(*), COUNT(*) FILTER (WHERE status = 'auto_resolved'), COUNT(*) FILTER (WHERE status = 'assigned_to_operator'), COUNT(*) FILTER (WHERE status = 'closed') FROM tickets`).Scan(&stats.Total, &stats.AutoResolved, &stats.Assigned, &stats.Closed); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load stats")
		return
	}
	if stats.Total > 0 {
		stats.Deflection = float64(stats.AutoResolved) * 100 / float64(stats.Total)
	}
	rows, err := h.db.Query(r.Context(), `SELECT department, COUNT(*) FROM tickets GROUP BY department`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load department stats")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var department string
		var count int
		if err := rows.Scan(&department, &count); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read stats")
			return
		}
		stats.ByDepartment[department] = count
	}
	channelRows, err := h.db.Query(r.Context(), `SELECT channel, COUNT(*) FROM tickets GROUP BY channel`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load channel stats")
		return
	}
	defer channelRows.Close()
	for channelRows.Next() {
		var channel string
		var count int
		if err := channelRows.Scan(&channel, &count); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read channel stats")
			return
		}
		stats.ByChannel[channel] = count
	}
	writeJSON(w, http.StatusOK, stats)
}

func ticketID(r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	return id, err == nil && id > 0
}
func validStatus(value string) bool {
	return hasValue(value, "new", "awaiting_confirmation", "assigned_to_operator", "auto_resolved", "closed")
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
       rag_answer, operator_reply, status, created_at
FROM tickets
ORDER BY created_at DESC, id DESC`

	rows, err := h.db.Query(r.Context(), query)
	if err != nil {
		h.logger.Error("failed to list tickets", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list tickets")
		return
	}
	defer rows.Close()

	tickets := make([]models.Ticket, 0)
	for rows.Next() {
		var ticket models.Ticket
		if err := rows.Scan(
			&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department,
			&ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply,
			&ticket.Status, &ticket.CreatedAt,
		); err != nil {
			h.logger.Error("failed to scan ticket", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to read tickets")
			return
		}
		tickets = append(tickets, ticket)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("failed while reading tickets", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to read tickets")
		return
	}

	writeJSON(w, http.StatusOK, tickets)
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
