package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
)

func TestUniversityContextPersistsAndRetriesDoNotDuplicate(t *testing.T) {
	db := replyTestDB(t)
	base, _ := knowledge.Load(t.TempDir())
	h := NewTicketsHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base))
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "employee-1", TenantID: "default", Role: "employee", Email: "synthetic@example.org"})
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/tickets", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", "university-test")
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.Create(w, r)
		return w
	}
	incomplete := `{"raw_text":"Не работает проектор","intake":{"location":"Корпус А"}}`
	if w := call(incomplete); w.Code != 400 {
		t.Fatalf("incomplete saved: %s", w.Body.String())
	}
	body := `{"raw_text":"Не работает проектор","intake":{"location":"Корпус А, аудитория 313","symptom":"Не включается","impact":"Занятие уже идёт","tried":"Пока ничего"}}`
	for i := 0; i < 2; i++ {
		if w := call(body); w.Code != 201 {
			t.Fatalf("create failed %d: %s", w.Code, w.Body.String())
		}
	}
	var count int
	var text, requester string
	if err := db.QueryRow(ctx, `SELECT COUNT(*),MIN(raw_text),MIN(requester_id) FROM tickets`).Scan(&count, &text, &requester); err != nil {
		t.Fatal(err)
	}
	if count != 1 || requester != "employee-1" || !strings.Contains(text, "аудитория 313") || !strings.Contains(text, "Занятие уже идёт") {
		t.Fatal("context, identity or idempotency lost")
	}
}
