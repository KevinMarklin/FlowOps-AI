package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"time"

	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
)

func (h *IntegrationsHandler) OrganizationConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || (p.Role != "admin" && !(p.Role == "analyst" && r.Method == "GET")) {
		writeError(w, 403, "Недостаточно прав")
		return
	}
	if !security.HasPermission(p, "settings_manage") && !security.HasPermission(p, "ai_configure") && !security.HasPermission(p, "automation_manage") {
		writeError(w, 403, "Недостаточно прав")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tenant := security.TenantID(ctx)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		c, err := settings.LoadPlatform(ctx, h.db, tenant)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить настройки")
			return
		}
		writeJSON(w, 200, c)
		return
	}
	var c settings.PlatformConfig
	if !decodeDialogue(w, r, &c) {
		return
	}
	previous, loadErr := settings.LoadPlatform(ctx, h.db, tenant)
	if loadErr != nil {
		writeError(w, 503, "Не удалось проверить настройки")
		return
	}
	if !reflect.DeepEqual(c.AI, previous.AI) && !security.HasPermission(p, "ai_configure") {
		writeError(w, 403, "Нет права на настройку ИИ")
		return
	}
	if !reflect.DeepEqual(c.Process, previous.Process) && !security.HasPermission(p, "automation_manage") {
		writeError(w, 403, "Нет права на настройку автоматизации")
		return
	}
	general := c
	general.AI = previous.AI
	general.Process = previous.Process
	general.Revision = previous.Revision
	if !reflect.DeepEqual(general, previous) && !security.HasPermission(p, "settings_manage") {
		writeError(w, 403, "Нет права на настройки организации")
		return
	}
	if err := c.Validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := h.validateProcessReferences(ctx, c); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить настройки")
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO app_settings(tenant_id) VALUES($1) ON CONFLICT DO NOTHING`, tenant); err != nil {
		writeError(w, 503, "Не удалось сохранить настройки")
		return
	}
	raw, _ := json.Marshal(c)
	tag, err := tx.Exec(ctx, `UPDATE app_settings SET platform_config=$2,platform_revision=platform_revision+1 WHERE tenant_id=$1 AND platform_revision=$3`, tenant, raw, c.Revision)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить настройки")
		return
	}
	if tag.RowsAffected() != 1 {
		writeError(w, 409, "Настройки уже изменены. Обновите их перед сохранением")
		return
	}
	tag, err = tx.Exec(ctx, `UPDATE workspaces SET name=$2 WHERE tenant_id=$1`, tenant, c.Name)
	if err != nil || tag.RowsAffected() != 1 {
		writeError(w, 503, "Организация не найдена")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'ORGANIZATION_SETTINGS_UPDATED',$2,$3)`, tenant, p.ID, "revision updated"); err != nil {
		writeError(w, 503, "Не удалось сохранить журнал изменений")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Не удалось сохранить настройки")
		return
	}
	c.Revision++
	writeJSON(w, 200, c)
}

func (h *IntegrationsHandler) OrganizationPublic(w http.ResponseWriter, r *http.Request) {
	if _, ok := security.CurrentPrincipal(r.Context()); !ok {
		writeError(w, 401, "Необходим вход")
		return
	}
	c, err := settings.LoadPlatform(r.Context(), h.db, security.TenantID(r.Context()))
	if err != nil {
		writeError(w, 503, "Не удалось загрузить организацию")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"name": c.Name, "description": c.Description, "industry": c.Industry, "timezone": c.Timezone, "contact_email": c.ContactEmail, "closure_reasons": c.ClosureReasons})
}

func (h *TicketsHandler) validClosureReason(ctx context.Context, id string) bool {
	c, err := settings.LoadPlatform(ctx, h.db, security.TenantID(ctx))
	if err != nil {
		return false
	}
	for _, reason := range c.ClosureReasons {
		if reason.ID == id {
			return true
		}
	}
	return false
}
