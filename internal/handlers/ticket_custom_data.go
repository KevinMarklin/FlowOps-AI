package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flowops-ai/internal/security"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

func (h *TicketsHandler) CustomFields(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	if !ok {
		writeError(w, 400, "Некорректный тикет")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tenant := security.TenantID(ctx)
	var raw []byte
	var revision int64
	if r.Method == "GET" {
		var snapshot, metadata []byte
		if err := h.db.QueryRow(ctx, `SELECT COALESCE(NULLIF(form_snapshot,'{}'),(SELECT jsonb_set(definition,'{revision}',to_jsonb(f.revision)) FROM organization_ticket_forms f WHERE f.tenant_id=t.tenant_id),'{}'),jsonb_build_object('category',category,'ticket_type',ticket_type,'priority_id',custom_priority,'tags',tags,'assignee_id',assignee_id,'workflow_status',workflow_status,'asset',asset_snapshot) FROM tickets t WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&snapshot, &metadata); err != nil {
			writeError(w, 404, "Тикет не найден")
			return
		}
		var definition TicketFormDefinition
		if json.Unmarshal(snapshot, &definition) != nil {
			writeError(w, 503, "Не удалось прочитать форму")
			return
		}
		if definition.Fields == nil {
			definition.Fields = []CustomField{}
		}
		err := h.db.QueryRow(ctx, `SELECT values,revision FROM ticket_custom_fields WHERE tenant_id=$1 AND ticket_id=$2`, tenant, id).Scan(&raw, &revision)
		if err != nil && err != pgx.ErrNoRows {
			writeError(w, 503, "Не удалось прочитать поля")
			return
		}
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		writeJSON(w, 200, map[string]any{"revision": revision, "values": json.RawMessage(raw), "form": definition, "metadata": json.RawMessage(metadata)})
		return
	}
	var input struct {
		Revision     int64          `json:"revision"`
		FormRevision int64          `json:"form_revision"`
		Values       map[string]any `json:"values"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil {
		writeError(w, 400, "Некорректные поля")
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить поля")
		return
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM tickets WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id).Scan(&status); err != nil {
		writeError(w, 404, "Тикет не найден")
		return
	}
	if dialogueClosed(status) {
		writeError(w, 409, "Тикет закрыт")
		return
	}
	var f TicketFormDefinition
	err = tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(form_snapshot,'{}'),(SELECT jsonb_set(definition,'{revision}',to_jsonb(f.revision)) FROM organization_ticket_forms f WHERE f.tenant_id=t.tenant_id),'{}') FROM tickets t WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&raw)
	if err != nil || json.Unmarshal(raw, &f) != nil {
		writeError(w, 503, "Не удалось прочитать форму")
		return
	}
	if f.Revision != input.FormRevision {
		writeError(w, 409, "Форма изменилась, обновите страницу")
		return
	}
	if err = validateFormValues(f, input.Values); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var previousRaw []byte
	if err = tx.QueryRow(ctx, `SELECT custom_data FROM tickets WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&previousRaw); err != nil {
		writeError(w, 503, "Не удалось прочитать данные")
		return
	}
	var previous map[string]any
	if json.Unmarshal(previousRaw, &previous) != nil {
		writeError(w, 503, "Не удалось прочитать данные")
		return
	}
	for _, field := range f.Fields {
		value, ok := input.Values[field.ID].(string)
		if !ok || value == "" {
			continue
		}
		if (field.Type == "member" || field.Type == "unit") && previous[field.ID] == value {
			continue
		}
		var exists bool
		switch field.Type {
		case "member":
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE tenant_id=$1 AND id=$2 AND is_active)`, tenant, value).Scan(&exists)
		case "unit":
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_units WHERE tenant_id=$1 AND id=$2 AND active)`, tenant, value).Scan(&exists)
		case "attachment":
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_attachments WHERE tenant_id=$1 AND ticket_id=$2 AND id=$3)`, tenant, id, value).Scan(&exists)
		default:
			continue
		}
		if err != nil || !exists {
			writeError(w, 400, "Связанный ресурс недоступен")
			return
		}
	}
	err = tx.QueryRow(ctx, `SELECT revision FROM ticket_custom_fields WHERE tenant_id=$1 AND ticket_id=$2`, tenant, id).Scan(&revision)
	if err != nil && err != pgx.ErrNoRows {
		writeError(w, 503, "Не удалось прочитать версию")
		return
	}
	if revision != input.Revision {
		writeError(w, 409, "Поля уже изменены")
		return
	}
	raw, _ = json.Marshal(input.Values)
	_, err = tx.Exec(ctx, `INSERT INTO ticket_custom_fields(tenant_id,ticket_id,revision,values) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,ticket_id) DO UPDATE SET revision=EXCLUDED.revision,values=EXCLUDED.values`, tenant, id, revision+1, raw)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить поля")
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE tickets SET custom_data=$3 WHERE tenant_id=$1 AND id=$2`, tenant, id, raw); err != nil {
		writeError(w, 503, "Не удалось сохранить данные")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,ticket_id,details) VALUES($1,'TICKET_FIELDS_UPDATED',$2,$3,'Custom fields saved')`, tenant, security.RequesterID(ctx), id); err != nil {
		writeError(w, 503, "Не удалось сохранить аудит")
		return
	}
	if tx.Commit(ctx) != nil {
		writeError(w, 503, "Не удалось сохранить поля")
		return
	}
	writeJSON(w, 200, map[string]any{"revision": revision + 1, "values": input.Values})
}
func (h *TicketsHandler) Attachments(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	if !ok {
		writeError(w, 400, "Некорректный тикет")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tenant := security.TenantID(ctx)
	if r.Method == "GET" {
		if attachment := r.PathValue("attachment"); attachment != "" {
			var filename, kind string
			var content []byte
			if h.db.QueryRow(ctx, `SELECT filename,mime,content FROM ticket_attachments WHERE tenant_id=$1 AND ticket_id=$2 AND id=$3`, tenant, id, attachment).Scan(&filename, &kind, &content) != nil {
				writeError(w, 404, "Вложение не найдено")
				return
			}
			w.Header().Set("Content-Type", kind)
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "sandbox")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(content)
			return
		}
		rows, err := h.db.Query(ctx, `SELECT id,filename,mime,octet_length(content),created_at FROM ticket_attachments WHERE tenant_id=$1 AND ticket_id=$2 ORDER BY created_at,id`, tenant, id)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить вложения")
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var key, name, kind string
			var size int
			var at time.Time
			if err = rows.Scan(&key, &name, &kind, &size, &at); err != nil {
				break
			}
			items = append(items, map[string]any{"id": key, "filename": name, "mime": kind, "size": size, "created_at": at})
		}
		if err != nil || rows.Err() != nil {
			writeError(w, 503, "Не удалось прочитать вложения")
			return
		}
		writeJSON(w, 200, items)
		return
	}
	content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil || len(content) == 0 {
		writeError(w, 400, "Файл должен быть от 1 байта до 5 МБ")
		return
	}
	kind := http.DetectContentType(content)
	if !hasValue(kind, "application/pdf", "image/png", "image/jpeg", "text/plain; charset=utf-8") {
		writeError(w, 400, "Разрешены PDF, PNG, JPEG и текст")
		return
	}
	decoded, decodeErr := url.PathUnescape(r.Header.Get("X-Filename"))
	if decodeErr != nil {
		writeError(w, 400, "Некорректное имя файла")
		return
	}
	name := filepath.Base(strings.ReplaceAll(decoded, "\\", "/"))
	if name == "." || name == "" || len([]rune(name)) > 180 || strings.ContainsAny(name, "\r\n") {
		writeError(w, 400, "Некорректное имя файла")
		return
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		writeError(w, 503, "Не удалось создать ID")
		return
	}
	key := hex.EncodeToString(random[:])
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить вложение")
		return
	}
	defer tx.Rollback(ctx)
	var status string
	if tx.QueryRow(ctx, `SELECT status FROM tickets WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, id).Scan(&status) != nil {
		writeError(w, 404, "Тикет не найден")
		return
	}
	if dialogueClosed(status) {
		writeError(w, 409, "Тикет закрыт")
		return
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM ticket_attachments WHERE tenant_id=$1 AND ticket_id=$2`, tenant, id).Scan(&count); err != nil || count >= 20 {
		writeError(w, 400, "Не более 20 вложений на тикет")
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO ticket_attachments(id,tenant_id,ticket_id,author_id,filename,mime,content) VALUES($1,$2,$3,$4,$5,$6,$7)`, key, tenant, id, security.RequesterID(ctx), name, kind, content)
	if err != nil || tx.Commit(ctx) != nil {
		writeError(w, 503, "Не удалось сохранить вложение")
		return
	}
	writeJSON(w, 201, map[string]any{"id": key, "filename": name, "mime": kind, "size": len(content), "url": fmt.Sprintf("/api/tickets/%d/attachments/%s", id, key)})
}
