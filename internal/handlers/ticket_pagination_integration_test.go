package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
)

func TestTicketListPaginationBoundsAndOwnership(t *testing.T) {
	db := replyTestDB(t)
	h := &TicketsHandler{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := security.WithPrincipal(context.Background(), security.Principal{ID: "page-owner", TenantID: "page-a", Role: "employee"})
	token := strings.Repeat("p", 40)
	sum := sha256.Sum256([]byte(token))
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('page-a','A','fixture','page-a-bootstrap'),('page-b','B','fixture','page-b-bootstrap')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('page-owner','page-a','Owner','owner@example.org','employee',$1)`, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	router := security.ProtectOperatorTenants(http.HandlerFunc(h.List), db)
	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest("GET", "/api/tickets?limit=200", nil))
	if unauthorized.Code != 401 {
		t.Fatal("anonymous history exposed")
	}

	if _, err := db.Exec(ctx, `INSERT INTO tickets(tenant_id,requester_id,user_email,raw_text,created_at) SELECT 'page-a','page-owner','fixture@example.org','Synthetic issue',NOW() + (n%3) * INTERVAL '1 hour' FROM generate_series(1,205) n; INSERT INTO tickets(tenant_id,requester_id,user_email,raw_text) VALUES('page-a','page-foreign','fixture@example.org','Private'),('page-b','page-owner','fixture@example.org','Other company')`); err != nil {
		t.Fatal(err)
	}
	request := func(query string) (*httptest.ResponseRecorder, []models.Ticket) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/tickets"+query, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		router.ServeHTTP(w, r)
		var tickets []models.Ticket
		if w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &tickets) != nil {
			t.Fatal("not an array response")
		}
		return w, tickets
	}
	for _, query := range []string{"?limit=0", "?limit=201", "?limit=-1", "?limit=oops", "?before_id=-1", "?before_id=oops", "?before_id=2147483648", "?limit=2&limit=3"} {
		if w, _ := request(query); w.Code != 400 {
			t.Errorf("query %s returned %d instead of 400", query, w.Code)
		}
	}
	w, tickets := request("")
	if w.Code != 200 || len(tickets) != 100 || w.Header().Get("X-Next-Cursor") != strconv.Itoa(tickets[99].ID) {
		t.Fatal("default page is not bounded with cursor")
	}
	if w, tickets := request("?limit=200"); w.Code != 200 || len(tickets) != 200 {
		t.Fatal("maximum valid page failed")
	}
	seen := map[int]bool{}
	cursor := ""
	for {
		query := "?limit=73"
		if cursor != "" {
			query += "&before_id=" + cursor
		}
		w, tickets := request(query)
		if w.Code != 200 {
			t.Fatalf("page failed: %d", w.Code)
		}
		previous := int(^uint(0) >> 1)
		for _, ticket := range tickets {
			if seen[ticket.ID] || ticket.ID >= previous || ticket.RawText != "Synthetic issue" {
				t.Fatal("duplicate, unsorted or foreign ticket")
			}
			seen[ticket.ID] = true
			previous = ticket.ID
		}
		next := w.Header().Get("X-Next-Cursor")
		if next == "" {
			break
		}
		if len(tickets) == 0 || next != fmt.Sprint(tickets[len(tickets)-1].ID) || next == cursor {
			t.Fatal("invalid next cursor")
		}
		cursor = next
	}
	if len(seen) != 205 {
		t.Fatalf("pagination lost tickets: got %d", len(seen))
	}
}
