package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"flowops-ai/internal/models"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
)

var errRequestConflict = errors.New("idempotency key already belongs to a different request")
var errRequestKey = errors.New("invalid idempotency key")
var requestKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
type creationGate struct {
	query                       rowQuerier
	tx                          pgx.Tx
	tenant, scope, key, payload string
	existing                    *models.Ticket
}

func requestHash(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

// The namespace is stable for a source bot, even if the chat later changes its
// website binding. A replay then fails closed on identity/payload mismatch.
func (h *TicketsHandler) beginCreation(ctx context.Context, namespace, scope, key string, payload any) (*creationGate, error) {
	g := &creationGate{query: h.db}
	if key == "" {
		return g, nil
	}
	if !requestKeyPattern.MatchString(key) {
		return nil, errRequestKey
	}
	encoded, err := json.Marshal(struct {
		Tenant, Requester string
		Request           any
	}{security.TenantID(ctx), security.RequesterID(ctx), payload})
	if err != nil {
		return nil, err
	}
	g.tenant, g.scope, g.key, g.payload = namespace, requestHash([]byte(scope)), requestHash([]byte(key)), requestHash(encoded)
	g.tx, err = h.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	g.query = g.tx
	if _, err = g.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, g.tenant+":"+g.scope+":"+g.key); err != nil {
		g.close(ctx)
		return nil, err
	}
	var previous string
	var id int
	err = g.tx.QueryRow(ctx, `SELECT payload_hash,ticket_id FROM ticket_requests WHERE tenant_id=$1 AND scope_hash=$2 AND key_hash=$3`, g.tenant, g.scope, g.key).Scan(&previous, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, nil
	}
	if err != nil {
		g.close(ctx)
		return nil, err
	}
	if previous != g.payload {
		g.close(ctx)
		return nil, errRequestConflict
	}
	ticket := models.Ticket{}
	err = g.tx.QueryRow(ctx, `SELECT id,user_email,COALESCE(channel,'web'),COALESCE(subject,''),COALESCE(callback_url,''),raw_text,department,priority,extracted_summary,rag_answer,operator_reply,pii_masked_count,pii_masked_types,status,created_at,delivery_status FROM tickets WHERE tenant_id=$1 AND id=$2 AND requester_id=$3`, security.TenantID(ctx), id, security.RequesterID(ctx)).Scan(&ticket.ID, &ticket.UserEmail, &ticket.Channel, &ticket.Subject, &ticket.CallbackURL, &ticket.RawText, &ticket.Department, &ticket.Priority, &ticket.ExtractedSummary, &ticket.RAGAnswer, &ticket.OperatorReply, &ticket.PIIMaskedCount, &ticket.PIIMaskedTypes, &ticket.Status, &ticket.CreatedAt, &ticket.DeliveryStatus)
	if err != nil {
		g.close(ctx)
		return nil, err
	}
	g.existing = &ticket
	return g, nil
}

func (g *creationGate) close(ctx context.Context) {
	if g != nil && g.tx != nil {
		_ = g.tx.Rollback(ctx)
	}
}
func (g *creationGate) finish(ctx context.Context, id int) error {
	if g.tx == nil {
		return nil
	}
	if _, err := g.tx.Exec(ctx, `INSERT INTO ticket_requests(tenant_id,scope_hash,key_hash,payload_hash,ticket_id) VALUES($1,$2,$3,$4,$5)`, g.tenant, g.scope, g.key, g.payload, id); err != nil {
		return err
	}
	return g.tx.Commit(ctx)
}

func writeCreationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRequestKey):
		writeError(w, 400, "Некорректный ключ повторной отправки")
	case errors.Is(err, errRequestConflict):
		writeError(w, 409, "Этот ключ уже использован для другого обращения или аккаунта")
	default:
		writeError(w, 503, "Не удалось подтвердить сохранение обращения. Повторите запрос с тем же ключом")
	}
}
