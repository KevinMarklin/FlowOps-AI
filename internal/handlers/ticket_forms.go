package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

func (h *IntegrationsHandler) TicketForm(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok {
		writeError(w, 401, "Необходим вход")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tenant := security.TenantID(ctx)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		var raw []byte
		var revision int64
		err := h.db.QueryRow(ctx, `SELECT definition,revision FROM organization_ticket_forms WHERE tenant_id=$1`, tenant).Scan(&raw, &revision)
		if err == pgx.ErrNoRows {
			writeJSON(w, 200, TicketFormDefinition{Fields: []CustomField{}})
			return
		}
		if err != nil {
			writeError(w, 503, "Не удалось загрузить форму")
			return
		}
		var f TicketFormDefinition
		if json.Unmarshal(raw, &f) != nil {
			writeError(w, 503, "Форма повреждена")
			return
		}
		f.Revision = revision
		writeJSON(w, 200, f)
		return
	}
	if p.Role != "admin" {
		writeError(w, 403, "Недостаточно прав")
		return
	}
	var f TicketFormDefinition
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&f) != nil {
		writeError(w, 400, "Некорректная форма")
		return
	}
	if err := validForm(f); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить форму")
		return
	}
	defer tx.Rollback(ctx)
	var lock string
	if err = tx.QueryRow(ctx, `SELECT tenant_id FROM workspaces WHERE tenant_id=$1 FOR UPDATE`, tenant).Scan(&lock); err != nil {
		writeError(w, 404, "Организация не найдена")
		return
	}
	var version int64
	err = tx.QueryRow(ctx, `SELECT revision FROM organization_ticket_forms WHERE tenant_id=$1`, tenant).Scan(&version)
	if err != nil && err != pgx.ErrNoRows {
		writeError(w, 503, "Не удалось прочитать версию")
		return
	}
	if version != f.Revision {
		writeError(w, 409, "Форма уже изменена")
		return
	}
	f.Revision = version + 1
	raw, _ := json.Marshal(f)
	_, err = tx.Exec(ctx, `INSERT INTO organization_ticket_forms(tenant_id,revision,definition) VALUES($1,$2,$3) ON CONFLICT(tenant_id) DO UPDATE SET revision=EXCLUDED.revision,definition=EXCLUDED.definition`, tenant, f.Revision, raw)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить форму")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'TICKET_FORM_UPDATED',$2,$3)`, tenant, p.ID, "form definition changed"); err != nil {
		writeError(w, 503, "Не удалось сохранить аудит")
		return
	}
	if tx.Commit(ctx) != nil {
		writeError(w, 503, "Не удалось сохранить форму")
		return
	}
	writeJSON(w, 200, f)
}
