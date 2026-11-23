package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flowops-ai/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("FLOWOPS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FLOWOPS_TEST_DATABASE_URL to isolated flowops_security_test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "flowops_security_test" {
		t.Fatal("integration tests require a dedicated flowops_security_test database")
	}
	admin, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flowops_delivery_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(context.Background(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if strings.HasPrefix(schema, "flowops_delivery_test_") {
			_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		}
		admin.Close()
	})
	for i := 0; i < 2; i++ {
		if err = database.Migrate(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func enqueueTestReply(t *testing.T, db *pgxpool.Pool, body string) int {
	t.Helper()
	ctx := context.Background()
	var id int
	if err := db.QueryRow(ctx, `INSERT INTO tickets(raw_text,operator_reply,status) VALUES('synthetic',$1,'resolved') RETURNING id`, body).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for i := 0; i < 2; i++ {
		if err = EnqueueReply(ctx, tx, id, "email", "employee@example.test", "https://hooks.example.test/reply", body); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertDelivery(t *testing.T, db *pgxpool.Pool, id int, wantStatus, wantDelivery string) {
	t.Helper()
	var status, state string
	if err := db.QueryRow(context.Background(), `SELECT status,delivery_status FROM tickets WHERE id=$1`, id).Scan(&status, &state); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || state != wantDelivery {
		t.Fatalf("got %s/%s want %s/%s", status, state, wantStatus, wantDelivery)
	}
}

func TestDurableReplyRetryAndRestart(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	id := enqueueTestReply(t, db, "synthetic answer")
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ticket_deliveries`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("dedupe failed: %d %v", count, err)
	}
	assertDelivery(t, db, id, "resolved", "pending")
	channelCalls, callbackCalls := 0, 0
	send := func(ctx context.Context, m Message) error {
		if m.Kind == "channel" {
			channelCalls++
			return nil
		}
		callbackCalls++
		if callbackCalls == 1 {
			return errors.New("temporary error")
		}
		return nil
	}
	q := New(db, send)
	for i := 0; i < 2; i++ {
		if _, err := q.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertDelivery(t, db, id, "resolved", "pending")
	if _, err := db.Exec(ctx, `UPDATE ticket_deliveries SET next_attempt_at=NOW() WHERE state='pending'`); err != nil {
		t.Fatal(err)
	}
	// Recreating the worker must not lose pending work or resend the successful channel.
	if _, err := New(db, send).ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertDelivery(t, db, id, "closed", "sent")
	if channelCalls != 1 || callbackCalls != 2 {
		t.Fatalf("wrong attempts %d/%d", channelCalls, callbackCalls)
	}
}

func TestDeliveryLeaseAndConcurrentWorkers(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	id := enqueueTestReply(t, db, "lease answer")
	if _, err := db.Exec(ctx, `UPDATE ticket_deliveries SET state='processing',attempts=1,locked_until=NOW()-INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	q := New(db, func(context.Context, Message) error { calls.Add(1); time.Sleep(20 * time.Millisecond); return nil })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := q.ProcessOnce(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("concurrent lease claimed same message: %d", calls.Load())
	}
	assertDelivery(t, db, id, "closed", "sent")
}

func TestFailedDeliveryAndTransactionRollback(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	id := enqueueTestReply(t, db, "failure answer")
	if _, err := db.Exec(ctx, `UPDATE ticket_deliveries SET attempts=9`); err != nil {
		t.Fatal(err)
	}
	q := New(db, func(context.Context, Message) error { return errors.New("unavailable") })
	for i := 0; i < 2; i++ {
		if _, err := q.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertDelivery(t, db, id, "resolved", "failed")
	tx, _ := db.Begin(ctx)
	_, _ = tx.Exec(ctx, `UPDATE tickets SET operator_reply='rolled back' WHERE id=$1`, id)
	if err := EnqueueReply(ctx, tx, id, "email", "other@example.test", "", "rolled back"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM ticket_deliveries WHERE body='rolled back'`).Scan(&count)
	if count != 0 {
		t.Fatal("rolled back reply left outgoing event")
	}
}

func TestReplyRevisionSupersedesOldWorkAndCanReturnToPreviousText(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	id := enqueueTestReply(t, db, "A")
	revise := func(body string) {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `UPDATE tickets SET operator_reply=$1,status='resolved' WHERE id=$2`, body, id); err != nil {
			t.Fatal(err)
		}
		if err = EnqueueReply(ctx, tx, id, "email", "employee@example.test", "https://hooks.example.test/reply", body); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	revise("B")
	var bodies []string
	q := New(db, func(_ context.Context, m Message) error { bodies = append(bodies, m.Body); return nil })
	for i := 0; i < 2; i++ {
		if _, err := q.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(bodies, ",") != "B,B" {
		t.Fatalf("superseded reply sent: %v", bodies)
	}
	assertDelivery(t, db, id, "closed", "sent")
	revise("A")
	assertDelivery(t, db, id, "resolved", "pending")
	for i := 0; i < 2; i++ {
		if _, err := q.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(bodies, ",") != "B,B,A,A" {
		t.Fatalf("new revision mistaken for old duplicate: %v", bodies)
	}
	assertDelivery(t, db, id, "closed", "sent")
}

func TestSentTextCanBeDeliveredAsANewRevision(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	id := enqueueTestReply(t, db, "A")
	var bodies []string
	q := New(db, func(_ context.Context, m Message) error { bodies = append(bodies, m.Body); return nil })
	drain := func() {
		for i := 0; i < 2; i++ {
			if _, err := q.ProcessOnce(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	drain()
	for _, body := range []string{"B", "A"} {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE tickets SET operator_reply=$1,status='resolved' WHERE id=$2`, body, id); err != nil {
			t.Fatal(err)
		}
		if err = EnqueueReply(ctx, tx, id, "email", "employee@example.test", "https://hooks.example.test/reply", body); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		assertDelivery(t, db, id, "resolved", "pending")
		drain()
	}
	if strings.Join(bodies, ",") != "A,A,B,B,A,A" {
		t.Fatalf("content-global deduplication lost new revision: %v", bodies)
	}
}
