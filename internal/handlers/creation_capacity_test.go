package handlers

import (
	"context"
	"flowops-ai/internal/analyzer"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentDistinctCreationLeavesCapacityForTenantPolicy(t *testing.T) {
	db := replyTestDB(t)
	cfg := db.Config().Copy()
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(context.Background(), `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('bounded-create','B','x','bounded-create')`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOWOPS_AI_PROVIDERS", "")
	base, _ := knowledge.Load(t.TempDir())
	NewKnowledgeHandler(base, pool).WithGovernedWrites()
	h := NewTicketsHandler(pool, slog.New(slog.NewTextHandler(io.Discard, nil)), analyzer.New(base).WithAIPolicy(pool))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	principal := security.Principal{ID: "bounded-person", Role: "employee", TenantID: "bounded-create", Email: "bounded@example.org"}
	results := make(chan *httptest.ResponseRecorder, 8)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			r := httptest.NewRequest("POST", "/api/tickets", strings.NewReader(`{"raw_text":"Synthetic capacity check"}`)).WithContext(security.WithPrincipal(ctx, principal))
			r.Header.Set("Idempotency-Key", "bounded-"+strconv.Itoa(i))
			w := httptest.NewRecorder()
			h.Create(w, r)
			results <- w
		}(i)
	}
	group.Wait()
	close(results)
	for w := range results {
		if w.Code != 201 {
			t.Fatal("creation exhausted policy connections", w.Code, w.Body.String())
		}
	}
	var n int
	if err = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM tickets WHERE tenant_id='bounded-create'`).Scan(&n); err != nil || n != 8 {
		t.Fatal(n, err)
	}
}
