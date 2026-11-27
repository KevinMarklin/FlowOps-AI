package handlers

import (
	"encoding/json"
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
	writeJSON(w, 200, intake.Evaluate(input.Message, input.Details))
}
