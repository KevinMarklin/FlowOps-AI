package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flowops-ai/internal/knowledge"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

type UnitMembership struct {
	MemberID string `json:"member_id"`
	Relation string `json:"relation"`
}
type OrganizationUnit struct {
	ID          string           `json:"id"`
	ParentID    string           `json:"parent_id"`
	Name        string           `json:"name"`
	Kind        string           `json:"kind"`
	RoutingCode string           `json:"routing_code"`
	Active      bool             `json:"active"`
	Revision    int64            `json:"revision"`
	Members     []UnitMembership `json:"members,omitempty"`
}

func (h *IntegrationsHandler) OrganizationUnits(w http.ResponseWriter, r *http.Request) {
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
		rows, err := h.db.Query(ctx, `SELECT id,COALESCE(parent_id,''),name,kind,routing_code,active,revision,COALESCE((SELECT jsonb_agg(jsonb_build_object('member_id',member_id,'relation',relation) ORDER BY member_id,relation) FROM organization_unit_members m WHERE m.tenant_id=u.tenant_id AND m.unit_id=u.id),'[]'::jsonb) FROM organization_units u WHERE tenant_id=$1 AND ($2 OR active) ORDER BY name,id LIMIT 5000`, tenant, p.Role == "admin")
		if err != nil {
			writeError(w, 503, "Не удалось загрузить структуру")
			return
		}
		defer rows.Close()
		units := []OrganizationUnit{}
		for rows.Next() {
			var u OrganizationUnit
			var raw []byte
			if err = rows.Scan(&u.ID, &u.ParentID, &u.Name, &u.Kind, &u.RoutingCode, &u.Active, &u.Revision, &raw); err != nil {
				break
			}
			if p.Role == "admin" {
				if err = json.Unmarshal(raw, &u.Members); err != nil {
					break
				}
			}
			units = append(units, u)
		}
		if err != nil || rows.Err() != nil {
			writeError(w, 503, "Не удалось прочитать структуру")
			return
		}
		writeJSON(w, 200, units)
		return
	}
	if p.Role != "admin" {
		writeError(w, 403, "Структурой управляет администратор")
		return
	}
	u := OrganizationUnit{Active: true}
	if !decodeDialogue(w, r, &u) {
		return
	}
	u.Name = strings.TrimSpace(u.Name)
	u.Kind = strings.TrimSpace(u.Kind)
	u.RoutingCode = strings.TrimSpace(u.RoutingCode)
	if u.Name == "" || len([]rune(u.Name)) > 160 || u.Kind == "" || len([]rune(u.Kind)) > 80 || len(u.ParentID) > 64 || len(u.Members) > 1000 || (u.RoutingCode != "" && !knowledge.ValidDepartmentName(u.RoutingCode)) {
		writeError(w, 400, "Некорректное подразделение")
		return
	}
	id := r.PathValue("id")
	creating := id == ""
	if creating {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			writeError(w, 503, "Не удалось создать ID")
			return
		}
		id = hex.EncodeToString(b[:])
	}
	u.ID = id
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить структуру")
		return
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT tenant_id FROM workspaces WHERE tenant_id=$1 FOR UPDATE`, tenant).Scan(&locked); err != nil {
		writeError(w, 404, "Организация не найдена")
		return
	}
	if !creating {
		var revision int64
		var oldCode string
		if err = tx.QueryRow(ctx, `SELECT revision,routing_code FROM organization_units WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&revision, &oldCode); err != nil {
			writeError(w, 404, "Подразделение не найдено")
			return
		}
		if revision != u.Revision {
			writeError(w, 409, "Подразделение уже изменено")
			return
		}
		if oldCode != "" && oldCode != u.RoutingCode {
			writeError(w, 400, "Код маршрутизации стабилен: создайте новое подразделение вместо замены кода")
			return
		}
	}
	if u.ParentID != "" {
		var allowed bool
		err = tx.QueryRow(ctx, `WITH RECURSIVE ancestors AS (SELECT id,parent_id FROM organization_units WHERE tenant_id=$1 AND id=$2 AND ($4=FALSE OR active) UNION ALL SELECT p.id,p.parent_id FROM organization_units p JOIN ancestors a ON a.parent_id=p.id WHERE p.tenant_id=$1) SELECT EXISTS(SELECT 1 FROM ancestors) AND NOT EXISTS(SELECT 1 FROM ancestors WHERE id=$3)`, tenant, u.ParentID, id, u.Active).Scan(&allowed)
		if err != nil || !allowed {
			writeError(w, 400, "Родитель не найден либо образуется цикл")
			return
		}
	}
	if creating {
		var count int
		if err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM organization_units WHERE tenant_id=$1`, tenant).Scan(&count); err != nil || count >= 5000 {
			writeError(w, 400, "Достигнут предел подразделений")
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO organization_units(tenant_id,id,parent_id,name,kind,routing_code,active) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7)`, tenant, id, u.ParentID, u.Name, u.Kind, u.RoutingCode, u.Active)
		u.Revision = 1
	} else {
		_, err = tx.Exec(ctx, `UPDATE organization_units SET parent_id=NULLIF($3,''),name=$4,kind=$5,routing_code=$6,active=$7,revision=revision+1 WHERE tenant_id=$1 AND id=$2`, tenant, id, u.ParentID, u.Name, u.Kind, u.RoutingCode, u.Active)
		u.Revision++
	}
	if err != nil {
		writeError(w, 400, "Не удалось сохранить: проверьте уникальность кода маршрутизации")
		return
	}
	if !u.Active {
		if _, err = tx.Exec(ctx, `WITH RECURSIVE subtree AS(SELECT id FROM organization_units WHERE tenant_id=$1 AND parent_id=$2 UNION ALL SELECT n.id FROM organization_units n JOIN subtree p ON n.parent_id=p.id WHERE n.tenant_id=$1) UPDATE organization_units SET active=FALSE,revision=revision+1 WHERE tenant_id=$1 AND id IN(SELECT id FROM subtree) AND active`, tenant, id); err != nil {
			writeError(w, 503, "Не удалось отключить дочерние подразделения")
			return
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM organization_unit_members WHERE tenant_id=$1 AND unit_id=$2`, tenant, id); err != nil {
		writeError(w, 503, "Не удалось сохранить состав")
		return
	}
	seen := map[string]bool{}
	for _, member := range u.Members {
		key := member.MemberID + ":" + member.Relation
		if !hasValue(member.Relation, "member", "manager", "deputy", "responsible") || seen[key] {
			writeError(w, 400, "Некорректные или повторяющиеся участники")
			return
		}
		seen[key] = true
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE tenant_id=$1 AND id=$2 AND is_active)`, tenant, member.MemberID).Scan(&exists); err != nil || !exists {
			writeError(w, 400, "Участник не найден в организации")
			return
		}
		if _, err = tx.Exec(ctx, `INSERT INTO organization_unit_members(tenant_id,unit_id,member_id,relation) VALUES($1,$2,$3,$4)`, tenant, id, member.MemberID, member.Relation); err != nil {
			writeError(w, 503, "Не удалось сохранить участника")
			return
		}
	}
	if err = syncUnitDepartments(ctx, tx, tenant); err != nil {
		writeError(w, 503, "Не удалось обновить маршрутизацию")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'ORGANIZATION_STRUCTURE_CHANGED',$2,$3)`, tenant, p.ID, id); err != nil {
		writeError(w, 503, "Не удалось сохранить аудит")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Не удалось сохранить структуру")
		return
	}
	code := 200
	if creating {
		code = 201
	}
	writeJSON(w, code, u)
}
func syncUnitDepartments(ctx context.Context, tx pgx.Tx, tenant string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO app_settings(tenant_id) VALUES($1) ON CONFLICT DO NOTHING`, tenant); err != nil {
		return err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT builder_config FROM app_settings WHERE tenant_id=$1 AND id=TRUE FOR UPDATE`, tenant).Scan(&raw); err != nil {
		return err
	}
	config, err := settings.LoadBuilderConfig(ctx, func(context.Context) ([]byte, error) { return raw, nil })
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT routing_code,name,active FROM organization_units WHERE tenant_id=$1 AND routing_code<>'' ORDER BY name,id`, tenant)
	if err != nil {
		return err
	}
	defer rows.Close()
	type route struct {
		name   string
		active bool
	}
	routes := map[string]route{}
	order := []string{}
	for rows.Next() {
		var code, name string
		var active bool
		if err = rows.Scan(&code, &name, &active); err != nil {
			return err
		}
		routes[code] = route{name, active}
		order = append(order, code)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	departments := []settings.Department{}
	for _, d := range config.Departments {
		if _, managed := routes[d.Name]; !managed {
			departments = append(departments, d)
		}
	}
	for _, code := range order {
		r := routes[code]
		if r.active {
			departments = append(departments, settings.Department{Name: code, Description: r.name})
		}
	}
	config.Departments = departments
	raw, err = json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE app_settings SET builder_config=$2 WHERE tenant_id=$1 AND id=TRUE`, tenant, raw)
	return err
}
