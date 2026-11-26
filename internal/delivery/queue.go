package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Message struct {
	ReplyVersion                   int
	ID                             int64
	TicketID                       int
	Kind, Channel, Recipient, Body string
	Attempt                        int
	TenantID                       string
}

// EnqueueReply runs in the same transaction as the ticket update. Each destination
// gets its own event, so a failed callback does not retry an already-sent email.
func EnqueueReply(ctx context.Context, tx pgx.Tx, ticketID int, channel, sender, callback, body string) error {
	bodyHash := sha256.Sum256([]byte(body))
	var version int
	if err := tx.QueryRow(ctx, `UPDATE tickets SET reply_version=reply_version+CASE WHEN reply_hash<>$2 THEN 1 ELSE 0 END,reply_hash=$2
 WHERE id=$1 RETURNING reply_version`, ticketID, hex.EncodeToString(bodyHash[:])).Scan(&version); err != nil {
		return err
	}
	// Deduplicate only within a revision. A -> B -> A is a new revision of A.
	if _, err := tx.Exec(ctx, `UPDATE ticket_deliveries SET state='superseded',locked_until=NULL
 WHERE ticket_id=$1 AND reply_version<>$2 AND state IN ('pending','processing','failed')`, ticketID, version); err != nil {
		return err
	}
	var messages []Message
	if channel == "email" || channel == "bot" {
		messages = append(messages, Message{TicketID: ticketID, Kind: "channel", Channel: channel, Recipient: sender, Body: body})
	}
	if callback != "" {
		messages = append(messages, Message{TicketID: ticketID, Kind: "callback", Recipient: callback, Body: body})
	}
	for _, m := range messages {
		hash := sha256.Sum256([]byte(m.Kind + "\x00" + m.Channel + "\x00" + m.Recipient + "\x00" + m.Body))
		_, err := tx.Exec(ctx, `INSERT INTO ticket_deliveries(ticket_id,kind,channel,recipient,body,dedupe_key,reply_version)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(ticket_id,reply_version,dedupe_key) DO UPDATE
		SET state='pending',attempts=0,next_attempt_at=NOW(),locked_until=NULL
		WHERE ticket_deliveries.state IN ('failed','superseded')`, m.TicketID, m.Kind, m.Channel, m.Recipient, m.Body, hex.EncodeToString(hash[:]), version)
		if err != nil {
			return err
		}
	}
	return RefreshTicket(ctx, tx, ticketID)
}

// Callers lock the ticket before modifying events; this keeps lock ordering the
// same between API transactions and delivery acknowledgements.
func RefreshTicket(ctx context.Context, tx pgx.Tx, ticketID int) error {
	_, err := tx.Exec(ctx, `WITH summary AS (
	 SELECT CASE
	 WHEN EXISTS(SELECT 1 FROM ticket_deliveries d WHERE d.ticket_id=t.id AND d.reply_version=t.reply_version AND d.state='failed') THEN 'failed'
	 WHEN EXISTS(SELECT 1 FROM ticket_deliveries d WHERE d.ticket_id=t.id AND d.reply_version=t.reply_version AND d.state IN ('pending','processing')) THEN 'pending'
	 WHEN EXISTS(SELECT 1 FROM ticket_deliveries d WHERE d.ticket_id=t.id AND d.reply_version=t.reply_version) THEN 'sent'
	 ELSE 'not_required' END AS result FROM tickets t WHERE t.id=$1)
	UPDATE tickets SET delivery_status=summary.result,
	status=CASE WHEN status IN ('resolved','closed') THEN CASE WHEN summary.result IN ('sent','not_required') THEN 'closed' ELSE 'resolved' END ELSE status END
	FROM summary WHERE tickets.id=$1`, ticketID)
	return err
}

type Queue struct {
	db   *pgxpool.Pool
	send func(context.Context, Message) error
}

func New(db *pgxpool.Pool, send func(context.Context, Message) error) *Queue {
	return &Queue{db: db, send: send}
}

func (q *Queue) ProcessOnce(ctx context.Context) (bool, error) {
	var m Message
	err := q.db.QueryRow(ctx, `WITH candidate AS (
	 SELECT id FROM ticket_deliveries
	 WHERE (state='pending' AND next_attempt_at<=NOW()) OR (state='processing' AND locked_until<NOW())
	 ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1)
	 UPDATE ticket_deliveries d SET state='processing',attempts=d.attempts+1,locked_until=NOW()+INTERVAL '1 minute'
	 FROM candidate WHERE d.id=candidate.id
	 RETURNING d.id,d.ticket_id,d.kind,d.channel,d.recipient,d.body,d.attempts,d.reply_version`).Scan(&m.ID, &m.TicketID, &m.Kind, &m.Channel, &m.Recipient, &m.Body, &m.Attempt, &m.ReplyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = q.db.QueryRow(ctx, `SELECT tenant_id FROM tickets WHERE id=$1`, m.TicketID).Scan(&m.TenantID); err != nil {
		return true, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	sendErr := q.send(sendCtx, m)
	cancel()
	// If the process stops before acknowledgement, the lease expires and another
	// worker retries. Delivery is at least once, not exactly once.
	tx, err := q.db.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM tickets WHERE id=$1 FOR UPDATE`, m.TicketID); err != nil {
		return true, err
	}
	state := "sent"
	if sendErr != nil {
		state = "pending"
		if m.Attempt >= 10 {
			state = "failed"
		}
	}
	seconds := int64(retryDelay(m.Attempt) / time.Second)
	tag, err := tx.Exec(ctx, `UPDATE ticket_deliveries SET state=$1,locked_until=NULL,next_attempt_at=NOW()+($2*INTERVAL '1 second')
	 WHERE id=$3 AND state='processing' AND attempts=$4`, state, seconds, m.ID, m.Attempt)
	if err != nil {
		return true, err
	}
	if tag.RowsAffected() > 0 {
		if err = RefreshTicket(ctx, tx, m.TicketID); err != nil {
			return true, err
		}
	}
	return true, tx.Commit(ctx)
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return time.Duration(1<<uint(attempt)) * time.Second
}

func (q *Queue) Run(ctx context.Context, log interface{ Error(string, ...any) }) {
	for ctx.Err() == nil {
		worked, err := q.ProcessOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Error("delivery queue failed")
		}
		if !worked || err != nil {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
