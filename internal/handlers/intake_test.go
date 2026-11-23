package handlers

import (
	"context"
	"strings"
	"testing"

	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

func TestUnsafeIntakeRejectedBeforeAnalysisOrDatabase(t *testing.T) {
	h := &TicketsHandler{} // Nil dependencies prove validation occurs before any side effects.
	for _, r := range []models.IngestRequest{
		{Channel: "api", Message: strings.Repeat("x", security.MaxTicketBytes+1)},
		{Channel: "email", Message: "VPN", Sender: strings.Repeat("x", 256)},
		{Channel: "api", Message: "VPN", CallbackURL: "https://127.0.0.1/internal"},
	} {
		if _, err := h.IngestMessage(context.Background(), r); err == nil {
			t.Fatalf("unsafe request accepted %+v", r)
		}
	}
}
