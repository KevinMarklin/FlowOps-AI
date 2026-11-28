package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"net/http"
	"strings"
	"time"
)

type AccessRole struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	BaseRole string   `json:"base_role"`
	Scope    string   `json:"scope"`
	UnitIDs  []string `json:"unit_ids"`
	Revision int64    `json:"revision"`
}

// AccessRoles stores policy definitions. Assignment/enforcement is deliberately
// separate: creating a definition must never silently grant resource access.
func (h *IntegrationsHandler) AccessRoles(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || p.Role != "admin" {
		writeError(w, 403, "Недостаточно прав")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tenant := security.TenantID(ctx)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		rows, err := h.db.Query(ctx, `SELECT id,name,base_role,scope,unit_ids,revision FROM organization_access_roles WHERE tenant_id=$1 ORDER BY name,id`, tenant)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить роли")
			return
		}
		defer rows.Close()
		roles := []AccessRole{}
		for rows.Next() {
			var role AccessRole
			var raw []byte
			if err = rows.Scan(&role.ID, &role.Name, &role.BaseRole, &role.Scope, &raw, &role.Revision); err != nil {
				break
			}
			if err = json.Unmarshal(raw, &role.UnitIDs); err != nil {
				break
			}
			roles = append(roles, role)
		}
		if err != nil || rows.Err() != nil {
			writeError(w, 503, "Не удалось прочитать роли")
			return
		}
		writeJSON(w, 200, map[string]any{"items": roles, "assignment_enabled": true})
		return
	}
	var role AccessRole
	if !decodeDialogue(w, r, &role) {
		return
	}
	role.Name = strings.TrimSpace(role.Name)
	if len(role.ID) < 1 || len(role.ID) > 64 || role.Name == "" || len([]rune(role.Name)) > 120 || !hasValue(role.BaseRole, "operator", "analyst", "employee") || !hasValue(role.Scope, "own", "organization", "units") || len(role.UnitIDs) > 100 || role.Revision < 0 {
		writeError(w, 400, "Некорректная роль")
		return
	}
	if role.Scope == "units" && len(role.UnitIDs) == 0 || role.Scope != "units" && len(role.UnitIDs) != 0 {
		writeError(w, 400, "Проверьте область подразделений")
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить роль")
		return
	}
	defer tx.Rollback(ctx)
	for _, id := range role.UnitIDs {
		var found bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_units WHERE tenant_id=$1 AND id=$2 AND active)`, tenant, id).Scan(&found); err != nil || !found {
			writeError(w, 400, "Подразделение недоступно")
			return
		}
	}
	seen := map[string]bool{}
	for _, id := range role.UnitIDs {
		if seen[id] {
			writeError(w, 400, "Подразделения не должны повторяться")
			return
		}
		seen[id] = true
	}
	if role.UnitIDs == nil {
		role.UnitIDs = []string{}
	}
	raw, _ := json.Marshal(role.UnitIDs)
	status := 201
	if role.Revision > 0 {
		err = tx.QueryRow(ctx, `UPDATE organization_access_roles SET name=$3,base_role=$4,scope=$5,unit_ids=$6,revision=revision+1 WHERE tenant_id=$1 AND id=$2 AND revision=$7 RETURNING revision`, tenant, role.ID, role.Name, role.BaseRole, role.Scope, raw, role.Revision).Scan(&role.Revision)
		status = 200
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO organization_access_roles(tenant_id,id,name,base_role,scope,unit_ids) SELECT $1,$2,$3,$4,$5,$6 WHERE $7=0 ON CONFLICT(tenant_id,id) DO NOTHING RETURNING revision`, tenant, role.ID, role.Name, role.BaseRole, role.Scope, raw, role.Revision).Scan(&role.Revision)
	}
	if err != nil {
		writeError(w, 409, "Роль уже существует или версия устарела")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'ACCESS_ROLE_DEFINED',$2,$3)`, tenant, p.ID, role.ID); err != nil {
		writeError(w, 503, "Не удалось сохранить аудит")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Не удалось сохранить роль")
		return
	}
	writeJSON(w, status, role)
}

func (h *IntegrationsHandler) AssignAccessRole(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || p.Role != "admin" {
		writeError(w, 403, "Недостаточно прав")
		return
	}
	var input struct {
		MemberID string `json:"member_id"`
		RoleID   string `json:"role_id"`
	}
	if !decodeDialogue(w, r, &input) {
		return
	}
	if input.MemberID == "" || input.MemberID == p.ID {
		writeError(w, 400, "Собственный доступ изменять нельзя")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tenant := security.TenantID(ctx)
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить доступ")
		return
	}
	defer tx.Rollback(ctx)
	var memberRole string
	if err = tx.QueryRow(ctx, `SELECT role FROM workspace_members WHERE tenant_id=$1 AND id=$2 AND is_active FOR UPDATE`, tenant, input.MemberID).Scan(&memberRole); err != nil || memberRole == "admin" {
		writeError(w, 400, "Участник недоступен для назначения")
		return
	}
	if input.RoleID == "" {
		_, err = tx.Exec(ctx, `DELETE FROM organization_member_roles WHERE tenant_id=$1 AND member_id=$2`, tenant, input.MemberID)
	} else {
		var base string
		if err = tx.QueryRow(ctx, `SELECT base_role FROM organization_access_roles WHERE tenant_id=$1 AND id=$2 FOR SHARE`, tenant, input.RoleID).Scan(&base); err != nil || base != memberRole {
			writeError(w, 400, "Базовая роль должна совпадать с ролью сотрудника")
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO organization_member_roles(tenant_id,member_id,role_id) VALUES($1,$2,$3) ON CONFLICT(tenant_id,member_id) DO UPDATE SET role_id=EXCLUDED.role_id`, tenant, input.MemberID, input.RoleID)
	}
	if err != nil {
		writeError(w, 503, "Не удалось назначить роль")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'ACCESS_ROLE_ASSIGNED',$2,$3)`, tenant, p.ID, input.MemberID+":"+input.RoleID); err != nil {
		writeError(w, 503, "Не удалось сохранить аудит")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Не удалось сохранить доступ")
		return
	}
	writeJSON(w, 200, map[string]bool{"updated": true})
}
