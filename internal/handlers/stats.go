package handlers

import (
	"math"
	"net/http"
	"os"
	"strconv"
	"time"

	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
)

type statsResponse struct {
	OperatorActiveSeconds   float64 `json:"operator_active_seconds"`
	OperatorMeasuredTickets int     `json:"operator_measured_tickets"`
	OperatorAverageSeconds  float64 `json:"operator_average_seconds"`
	RoutingCorrectedTickets int     `json:"routing_corrected_tickets"`
	RoutingCorrections      int     `json:"routing_corrections"`

	Total                int            `json:"total"`
	AutoResolved         int            `json:"auto_resolved"`
	Assigned             int            `json:"assigned_to_operator"`
	Closed               int            `json:"closed"`
	Open                 int            `json:"open"`
	AwaitingConfirmation int            `json:"awaiting_confirmation"`
	Deflection           float64        `json:"deflection_rate_percent"`
	EstimatedSavedHours  float64        `json:"estimated_saved_hours"`
	BaselineMinutes      float64        `json:"baseline_minutes_per_ticket"`
	SavingsAreEstimate   bool           `json:"savings_are_estimate"`
	ByDepartment         map[string]int `json:"by_department"`
	ActiveByDepartment   map[string]int `json:"active_by_department"`
	ByChannel            map[string]int `json:"by_channel"`
	AIMode               string         `json:"ai_mode"`
	AIModel              string         `json:"ai_model"`
	AIProvider           string         `json:"ai_provider"`
	TotalPIIMasked       int            `json:"total_pii_masked"`
	SecurityContour      string         `json:"security_contour"`
	ActiveChannels       []string       `json:"active_channels"`
	ObservedAt           time.Time      `json:"observed_at"`
}

// A demonstration assumption, not measured handling time. Exposed with every
// result so the estimate can be reproduced and replaced with pilot measurements.
func supportBaselineMinutes() float64 {
	value, err := strconv.ParseFloat(os.Getenv("FLOWOPS_BASELINE_MINUTES"), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 480 {
		return 10
	}
	return value
}

func (h *TicketsHandler) Stats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	stats := statsResponse{ByDepartment: map[string]int{}, ActiveByDepartment: map[string]int{}, ByChannel: map[string]int{}, BaselineMinutes: supportBaselineMinutes(), SavingsAreEstimate: true}
	stats.AIMode, stats.AIProvider, stats.AIModel = h.analyzer.ActiveProvider()
	stats.SecurityContour = "Маскирование перед внешней моделью"
	stats.ActiveChannels = []string{"Web"}
	if h.dispatcher != nil {
		stats.ActiveChannels = h.dispatcher.EnabledChannels()
	}
	// All cards and departmental counts must describe the same PostgreSQL snapshot.
	tx, err := h.db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeError(w, 500, "failed to load stats")
		return
	}
	defer tx.Rollback(r.Context())
	err = tx.QueryRow(r.Context(), `SELECT COUNT(*),
	 COUNT(*) FILTER (WHERE status='auto_resolved' AND NULLIF(BTRIM(rag_answer),'') IS NOT NULL AND NULLIF(BTRIM(operator_reply),'') IS NULL
	 AND NOT EXISTS(SELECT 1 FROM audit_logs a WHERE a.ticket_id=tickets.id AND a.tenant_id=tickets.tenant_id AND a.event_type='OPERATOR_REPLY' AND a.created_at>=tickets.created_at)
 AND NOT EXISTS(SELECT 1 FROM ticket_work_sessions w WHERE w.ticket_id=tickets.id AND w.tenant_id=tickets.tenant_id AND (w.running OR w.active_seconds>0))),
	 COUNT(*) FILTER (WHERE status='assigned_to_operator'),
	 COUNT(*) FILTER (WHERE status='closed'),
	 COUNT(*) FILTER (WHERE status IN ('new','awaiting_confirmation','assigned_to_operator','resolved')),
	 COUNT(*) FILTER (WHERE status='awaiting_confirmation'),
	 COALESCE(SUM(pii_masked_count),0),CURRENT_TIMESTAMP FROM tickets WHERE tenant_id=$1`, security.TenantID(r.Context())).Scan(&stats.Total, &stats.AutoResolved, &stats.Assigned, &stats.Closed, &stats.Open, &stats.AwaitingConfirmation, &stats.TotalPIIMasked, &stats.ObservedAt)
	if err != nil {
		writeError(w, 500, "failed to load stats")
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT COALESCE(NULLIF(department,''),'Unassigned'),COUNT(*),
	 COUNT(*) FILTER (WHERE status IN ('new','awaiting_confirmation','assigned_to_operator','resolved')) FROM tickets WHERE tenant_id=$1 GROUP BY 1`, security.TenantID(r.Context()))
	if err != nil {
		writeError(w, 500, "failed to load department stats")
		return
	}
	for rows.Next() {
		var department string
		var total, open int
		if err = rows.Scan(&department, &total, &open); err != nil {
			break
		}
		stats.ByDepartment[department] = total
		stats.ActiveByDepartment[department] = open
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		writeError(w, 500, "failed to read department stats")
		return
	}
	rows, err = tx.Query(r.Context(), `SELECT COALESCE(NULLIF(channel,''),'web'),COUNT(*) FROM tickets WHERE tenant_id=$1 GROUP BY 1`, security.TenantID(r.Context()))
	if err != nil {
		writeError(w, 500, "failed to load channel stats")
		return
	}
	for rows.Next() {
		var channel string
		var count int
		if err = rows.Scan(&channel, &count); err != nil {
			break
		}
		stats.ByChannel[channel] = count
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		writeError(w, 500, "failed to read channel stats")
		return
	}
	if err = tx.QueryRow(r.Context(), `SELECT COALESCE(SUM(active_seconds),0),COUNT(DISTINCT ticket_id) FILTER(WHERE active_seconds>0) FROM ticket_work_sessions WHERE tenant_id=$1`, security.TenantID(r.Context())).Scan(&stats.OperatorActiveSeconds, &stats.OperatorMeasuredTickets); err != nil {
		writeError(w, 500, "failed to load operator work stats")
		return
	}
	if stats.OperatorMeasuredTickets > 0 {
		stats.OperatorAverageSeconds = stats.OperatorActiveSeconds / float64(stats.OperatorMeasuredTickets)
	}
	if err = tx.QueryRow(r.Context(), `SELECT COUNT(DISTINCT a.ticket_id),COUNT(*) FROM audit_logs a JOIN tickets t ON t.id=a.ticket_id AND t.tenant_id=a.tenant_id WHERE a.tenant_id=$1 AND a.event_type='TICKET_ROUTING_CORRECTED'`, security.TenantID(r.Context())).Scan(&stats.RoutingCorrectedTickets, &stats.RoutingCorrections); err != nil {
		writeError(w, 500, "failed to load routing corrections")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to complete stats snapshot")
		return
	}
	if stats.Total > 0 {
		stats.Deflection = float64(stats.AutoResolved) * 100 / float64(stats.Total)
	}
	stats.EstimatedSavedHours = float64(stats.AutoResolved) * stats.BaselineMinutes / 60
	writeJSON(w, http.StatusOK, stats)
}
