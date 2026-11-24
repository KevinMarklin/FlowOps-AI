package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http/httptest"
	"testing"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
)

func TestStatsUseConfirmedAutomationAndDepartmentLoad(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	t.Setenv("FLOWOPS_BASELINE_MINUTES", "12")
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	base, err := knowledge.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	read := func() statsResponse {
		t.Helper()
		w := httptest.NewRecorder()
		h.Stats(w, httptest.NewRequest("GET", "/api/stats", nil))
		if w.Code != 200 {
			t.Fatalf("stats failed: %s", w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("stats may be cached")
		}
		var s statsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := read(); s.Total != 0 || s.Deflection != 0 || s.EstimatedSavedHours != 0 || s.ObservedAt.IsZero() {
		t.Fatalf("bad empty stats %+v", s)
	}
	for _, tc := range []struct {
		status, department string
		rag, reply         any
	}{
		{"auto_resolved", "HR", "guide", nil},
		{"auto_resolved", "HR", "guide", "operator answer"},
		{"auto_resolved", "General", nil, nil},
		{"awaiting_confirmation", "IT", "guide", nil},
		{"assigned_to_operator", "Accounting", nil, nil},
		{"closed", "Billing", nil, "operator answer"},
		{"new", "", nil, nil},
		{"resolved", "HR", nil, "operator answer"},
	} {
		if _, err := db.Exec(ctx, `INSERT INTO tickets(raw_text,status,department,rag_answer,operator_reply) VALUES('synthetic',$1,$2,$3,$4)`, tc.status, tc.department, tc.rag, tc.reply); err != nil {
			t.Fatal(err)
		}
	}
	s := read()
	if s.Total != 8 || s.AutoResolved != 1 || s.Deflection != 12.5 || math.Abs(s.EstimatedSavedHours-0.2) > 0.00001 || s.Open != 4 || s.AwaitingConfirmation != 1 {
		t.Fatalf("incorrect stats %+v", s)
	}
	if s.ActiveByDepartment["Unassigned"] != 1 || s.ActiveByDepartment["Accounting"] != 1 || s.ByDepartment["HR"] != 3 {
		t.Fatalf("wrong load %+v", s)
	}
	// Audit entries left by a demo reset must not attach to a newer reused ID.
	if _, err := db.Exec(ctx, `INSERT INTO audit_logs(event_type,actor,ticket_id,created_at) VALUES('OPERATOR_REPLY','operator',1,NOW()-INTERVAL '1 day')`); err != nil {
		t.Fatal(err)
	}
	if read().AutoResolved != 1 {
		t.Fatal("historic audit changed current-ticket deflection")
	}
	// A live feedback transition updates both percentage and savings on next request.
	if _, err := db.Exec(ctx, `UPDATE tickets SET status='auto_resolved' WHERE status='awaiting_confirmation'`); err != nil {
		t.Fatal(err)
	}
	s = read()
	if s.AutoResolved != 2 || s.Deflection != 25 || math.Abs(s.EstimatedSavedHours-0.4) > 0.00001 {
		t.Fatalf("not live %+v", s)
	}
	if _, err := db.Exec(ctx, `INSERT INTO audit_logs(event_type,actor,ticket_id) VALUES('OPERATOR_REPLY','operator',1)`); err != nil {
		t.Fatal(err)
	}
	if read().AutoResolved != 1 {
		t.Fatal("operator-assisted ticket counted as deflection")
	}
}

func TestBaselineRejectsInvalidAssumptions(t *testing.T) {
	for _, value := range []string{"", "NaN", "Inf", "-1", "0", "481", "broken"} {
		t.Setenv("FLOWOPS_BASELINE_MINUTES", value)
		if supportBaselineMinutes() != 10 {
			t.Errorf("invalid baseline accepted %s", value)
		}
	}
	t.Setenv("FLOWOPS_BASELINE_MINUTES", "7.5")
	if supportBaselineMinutes() != 7.5 {
		t.Fatal("valid baseline rejected")
	}
}
