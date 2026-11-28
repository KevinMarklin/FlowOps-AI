package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/settings"
	"io"
	"net/http"

	"flowops-ai/internal/intake"
	"flowops-ai/internal/security"
)

// IntakePreview is read-only: no model, database write or external request.
func (h *TicketsHandler) IntakePreview(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Message string         `json:"message"`
		Details intake.Details `json:"details"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, security.MaxTicketBytes+8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, 400, "Некорректное обращение.")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "Ожидается одно обращение.")
		return
	}
	if err := intake.Validate(input.Message, input.Details); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, h.IntakePlan(r.Context(), input.Message, input.Details))
}

func (h *TicketsHandler) IntakePlan(ctx context.Context, message string, details intake.Details) intake.Plan {
	if h.db == nil {
		return intake.Evaluate(message, details)
	}
	cfg, err := settings.LoadPlatform(ctx, h.db, security.TenantID(ctx))
	if err != nil {
		return intake.Plan{Ready: true, Fields: []intake.Field{}}
	}
	limit := -1
	if cfg.AI.Mode != "" {
		limit = cfg.AI.ClarificationLimit
	}
	return intake.EvaluateWithLimit(message, details, limit)
}
func (h *TicketsHandler) BotIntakePlan(ctx context.Context, sender, message string, details intake.Details) intake.Plan {
	bound, err := security.BotContext(ctx, h.db, sender)
	if err == nil {
		ctx = bound
	}
	return h.IntakePlan(ctx, message, details)
}
