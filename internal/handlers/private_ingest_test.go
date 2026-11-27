package handlers

import (
	"flowops-ai/internal/models"
	"testing"
)

func TestCorporateGroupTicketRejectedBeforeAnalysis(t *testing.T) {
	h := &TicketsHandler{}
	if err := h.validateIngest(models.IngestRequest{Channel: "bot", Sender: "max_chat:123", Message: "confidential issue"}); err == nil {
		t.Fatal("group ticket accepted")
	}
}
