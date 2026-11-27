package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flowops-ai/internal/security"
)

func TestBotLinksStressConcurrentSingleUse(t *testing.T) {
	db := replyTestDB(t)
	ctx := security.WithTenantID(context.Background(), "default")
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('stress-links','Synthetic stress','fixture','stress-hash')`); err != nil {
		t.Fatal(err)
	}
	const members, attempts, workers = 64, 4, 16
	for i := 0; i < members; i++ {
		id := fmt.Sprintf("stress-%d", i)
		code := fmt.Sprintf("link_%043d", i)
		hash := sha256.Sum256([]byte(code))
		if _, err := db.Exec(ctx, `INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES($1,'stress-links','Synthetic',$2,'employee',$3)`, id, id+"@example.org", id+"-hash"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO bot_link_codes(token_hash,member_id,channel,auth_version,expires_at) VALUES($1,$2,'telegram',0,NOW()+INTERVAL '10 minutes')`, hex.EncodeToString(hash[:]), id); err != nil {
			t.Fatal(err)
		}
	}
	jobs := make(chan int, members*attempts)
	for j := 0; j < attempts; j++ {
		for i := 0; i < members; i++ {
			jobs <- i
		}
	}
	close(jobs)
	var success atomic.Int64
	var wg sync.WaitGroup
	var mu sync.Mutex
	var times []float64
	started := time.Now()
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				start := time.Now()
				err := security.LinkBotAccount(ctx, db, fmt.Sprintf("tg:%d", 10000+i), fmt.Sprintf("link_%043d", i))
				if err == nil {
					success.Add(1)
				}
				mu.Lock()
				times = append(times, float64(time.Since(start).Microseconds())/1000)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if success.Load() != members {
		t.Fatalf("single-use violated: got %d successful claims, want %d", success.Load(), members)
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM member_bot_links WHERE member_id LIKE 'stress-%'`).Scan(&count); err != nil || count != members {
		t.Fatalf("lost links: %d %v", count, err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM bot_link_codes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("codes retained: %d %v", count, err)
	}
	sort.Float64s(times)
	t.Logf("STRESS workers=%d requests=%d successful=%d rejected=%d median_ms=%.2f p95_ms=%.2f max_ms=%.2f elapsed_ms=%d", workers, len(times), success.Load(), len(times)-int(success.Load()), times[len(times)/2], times[(len(times)-1)*95/100], times[len(times)-1], time.Since(started).Milliseconds())
}
