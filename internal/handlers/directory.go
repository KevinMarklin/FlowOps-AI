package handlers

import (
	"flowops-ai/internal/security"
	"net/http"
)

func (h *IntegrationsHandler) Directory(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok {
		writeError(w, 401, "Необходим вход")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	all := p.Role == "admin"
	if p.Permissions != nil {
		all = false
		for _, permission := range *p.Permissions {
			if permission == "members_manage" && p.Role == "admin" {
				all = true
			}
		}
	}
	var restricted bool
	if err := h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM organization_member_roles m JOIN organization_access_roles a ON a.tenant_id=m.tenant_id AND a.id=m.role_id WHERE m.tenant_id=$1 AND m.member_id=$2 AND a.scope<>'organization')`, p.TenantID, p.ID).Scan(&restricted); err != nil {
		writeError(w, 503, "Не удалось проверить область доступа")
		return
	}
	all = all && !restricted
	rows, err := h.db.Query(r.Context(), `SELECT id,display_name,role FROM workspace_members WHERE tenant_id=$1 AND is_active AND ($3 OR id=$2) ORDER BY display_name,id LIMIT 5000`, p.TenantID, p.ID, all)
	if err != nil {
		writeError(w, 503, "Не удалось загрузить справочник")
		return
	}
	members := []map[string]string{}
	for rows.Next() {
		var id, name, role string
		if err = rows.Scan(&id, &name, &role); err != nil {
			break
		}
		members = append(members, map[string]string{"id": id, "name": name, "role": role})
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		writeError(w, 503, "Не удалось прочитать справочник")
		return
	}
	rows, err = h.db.Query(r.Context(), `SELECT u.id,u.name FROM organization_units u WHERE u.tenant_id=$1 AND u.active AND ($3 OR EXISTS(SELECT 1 FROM organization_unit_members m WHERE m.tenant_id=u.tenant_id AND m.unit_id=u.id AND m.member_id=$2)) ORDER BY u.name,u.id LIMIT 5000`, p.TenantID, p.ID, all)
	if err != nil {
		writeError(w, 503, "Не удалось загрузить подразделения")
		return
	}
	defer rows.Close()
	units := []map[string]string{}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			break
		}
		units = append(units, map[string]string{"id": id, "name": name})
	}
	if err != nil || rows.Err() != nil {
		writeError(w, 503, "Не удалось прочитать подразделения")
		return
	}
	writeJSON(w, 200, map[string]any{"members": members, "units": units})
}
