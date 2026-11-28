package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

type DashboardWidget struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Type      string            `json:"type"`
	Metric    string            `json:"metric"`
	Dimension string            `json:"dimension"`
	Measure   string            `json:"measure"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Filters   map[string]string `json:"filters"`
}
type DashboardConfig struct {
	Filters map[string]string `json:"filters"`
	Widgets []DashboardWidget `json:"widgets"`
}
type Dashboard struct {
	ID        string          `json:"id"`
	Owner     string          `json:"owner_id"`
	Name      string          `json:"name"`
	Shared    bool            `json:"shared"`
	Revision  int64           `json:"revision"`
	Config    DashboardConfig `json:"config"`
	UpdatedAt time.Time       `json:"updated_at"`
}

var dashboardMetrics = map[string]bool{"total": true, "new": true, "active": true, "closed": true, "overdue": true, "reopens": true, "redirects": true, "first_response_mean": true, "first_response_median": true, "resolution_mean": true, "resolution_median": true, "active_hours": true, "deflection": true, "ai_share": true, "sla_compliance": true, "first_contact_resolution": true, "satisfaction": true, "released_hours": true, "released_time_value": true, "ai_cost": true, "cost_per_ticket": true, "routing_accuracy": true}

func validDashboardFilters(filters map[string]string) bool {
	if len(filters) > 10 {
		return false
	}
	for key, value := range filters {
		if !hasValue(key, "from", "to", "department", "status", "priority", "channel", "requester", "processing", "sla") || len(value) > 128 {
			return false
		}
	}
	return true
}
func validDashboard(d Dashboard) bool {
	if strings.TrimSpace(d.Name) == "" || len([]rune(d.Name)) > 160 || len(d.Config.Widgets) > 30 || !validDashboardFilters(d.Config.Filters) {
		return false
	}
	ids := map[string]bool{}
	for _, w := range d.Config.Widgets {
		if w.ID == "" || len(w.ID) > 64 || ids[w.ID] || len([]rune(w.Title)) > 160 || !hasValue(w.Type, "kpi", "bar", "line", "pie", "distribution", "heatmap", "funnel", "table", "load") || !dashboardMetrics[w.Metric] || !hasValue(w.Dimension, "department", "day", "channel") || !hasValue(w.Measure, "total", "open", "closed") || w.Width < 1 || w.Width > 4 || w.Height < 1 || w.Height > 4 || !validDashboardFilters(w.Filters) {
			return false
		}
		ids[w.ID] = true
	}
	return true
}
func (h *TicketsHandler) Dashboards(w http.ResponseWriter, r *http.Request) {
	p, ok := analyticsPrincipal(w, r)
	if !ok {
		return
	}
	tenant := security.TenantID(r.Context())
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		rows, err := h.db.Query(r.Context(), `SELECT id,owner_id,name,shared,revision,config,updated_at FROM analytics_dashboards WHERE tenant_id=$1 AND (owner_id=$2 OR shared) ORDER BY updated_at DESC LIMIT 200`, tenant, p.ID)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить дашборды")
			return
		}
		defer rows.Close()
		result := []Dashboard{}
		for rows.Next() {
			var d Dashboard
			var raw []byte
			if err = rows.Scan(&d.ID, &d.Owner, &d.Name, &d.Shared, &d.Revision, &raw, &d.UpdatedAt); err != nil {
				break
			}
			if err = json.Unmarshal(raw, &d.Config); err != nil {
				break
			}
			result = append(result, d)
		}
		if err != nil || rows.Err() != nil {
			writeError(w, 503, "Не удалось прочитать дашборды")
			return
		}
		writeJSON(w, 200, result)
		return
	}
	var d Dashboard
	if !decodeDialogue(w, r, &d) {
		return
	}
	if !validDashboard(d) {
		writeError(w, 400, "Некорректная конфигурация дашборда")
		return
	}
	if d.Shared && p.Role != "admin" {
		writeError(w, 403, "Общие дашборды создаёт администратор")
		return
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		writeError(w, 503, "Не удалось создать ID")
		return
	}
	d.ID = hex.EncodeToString(id[:])
	d.Owner = p.ID
	raw, _ := json.Marshal(d.Config)
	tx, err := h.beginTicketTx(r.Context())
	if err != nil {
		writeError(w, 503, "Не удалось сохранить дашборд")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT tenant_id FROM workspaces WHERE tenant_id=$1 FOR UPDATE`, tenant); err != nil {
		writeError(w, 503, "Не удалось сохранить дашборд")
		return
	}
	var count int
	if err = tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM analytics_dashboards WHERE tenant_id=$1 AND owner_id=$2`, tenant, p.ID).Scan(&count); err != nil || count >= 100 {
		writeError(w, 400, "Можно сохранить не более 100 дашбордов")
		return
	}
	err = tx.QueryRow(r.Context(), `INSERT INTO analytics_dashboards(id,tenant_id,owner_id,name,shared,config) VALUES($1,$2,$3,$4,$5,$6) RETURNING revision,updated_at`, d.ID, tenant, p.ID, d.Name, d.Shared, raw).Scan(&d.Revision, &d.UpdatedAt)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить дашборд")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'DASHBOARD_CREATED',$2,$3)`, tenant, p.ID, d.ID); err != nil {
		writeError(w, 503, "Не удалось записать аудит")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 503, "Не удалось сохранить дашборд")
		return
	}
	writeJSON(w, 201, d)
}
func (h *TicketsHandler) ChangeDashboard(w http.ResponseWriter, r *http.Request) {
	p, ok := analyticsPrincipal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	tenant := security.TenantID(r.Context())
	tx, err := h.beginTicketTx(r.Context())
	if err != nil {
		writeError(w, 503, "Дашборд недоступен")
		return
	}
	defer tx.Rollback(r.Context())
	var old Dashboard
	var raw []byte
	err = tx.QueryRow(r.Context(), `SELECT id,owner_id,name,shared,revision,config,updated_at FROM analytics_dashboards WHERE tenant_id=$1 AND id=$2 AND (owner_id=$3 OR (shared AND $4)) FOR UPDATE`, tenant, id, p.ID, p.Role == "admin").Scan(&old.ID, &old.Owner, &old.Name, &old.Shared, &old.Revision, &raw, &old.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, 404, "Дашборд недоступен")
		} else {
			writeError(w, 503, "Дашборд недоступен")
		}
		return
	}
	if r.Method == "DELETE" {
		var request struct {
			Revision int64 `json:"revision"`
		}
		if !decodeDialogue(w, r, &request) {
			return
		}
		if request.Revision != old.Revision {
			writeError(w, 409, "Дашборд уже изменён")
			return
		}
		if _, err = tx.Exec(r.Context(), `DELETE FROM analytics_dashboards WHERE tenant_id=$1 AND id=$2`, tenant, id); err != nil {
			writeError(w, 503, "Не удалось удалить дашборд")
			return
		}
	} else {
		var d Dashboard
		if !decodeDialogue(w, r, &d) {
			return
		}
		if !validDashboard(d) {
			writeError(w, 400, "Некорректная конфигурация")
			return
		}
		if d.Revision != old.Revision {
			writeError(w, 409, "Дашборд уже изменён. Обновите список")
			return
		}
		if d.Shared && p.Role != "admin" {
			writeError(w, 403, "Нет прав на общий дашборд")
			return
		}
		raw, _ = json.Marshal(d.Config)
		err = tx.QueryRow(r.Context(), `UPDATE analytics_dashboards SET name=$3,shared=$4,config=$5,revision=revision+1,updated_at=NOW() WHERE tenant_id=$1 AND id=$2 RETURNING revision,updated_at`, tenant, id, d.Name, d.Shared, raw).Scan(&d.Revision, &d.UpdatedAt)
		if err != nil {
			writeError(w, 503, "Не удалось сохранить дашборд")
			return
		}
		d.ID = id
		d.Owner = old.Owner
		old = d
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'DASHBOARD_CHANGED',$2,$3)`, tenant, p.ID, id); err != nil {
		writeError(w, 503, "Не удалось записать аудит")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, 503, "Не удалось сохранить дашборд")
		return
	}
	writeJSON(w, 200, old)
}
