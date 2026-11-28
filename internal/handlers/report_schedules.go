package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ReportSchedule struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Filters      map[string]string `json:"filters"`
	Format       string            `json:"format"`
	PeriodDays   int               `json:"period_days"`
	IntervalDays int               `json:"interval_days"`
	Active       bool              `json:"active"`
	Revision     int64             `json:"revision"`
	NextRun      time.Time         `json:"next_run"`
	LastError    string            `json:"last_error"`
}

func validSchedule(s ReportSchedule) bool {
	if strings.TrimSpace(s.Name) == "" || len([]rune(s.Name)) > 160 || !hasValue(s.Format, "csv", "xlsx", "pdf") || s.PeriodDays < 1 || s.PeriodDays > 365 || s.IntervalDays < 1 || s.IntervalDays > 365 || len(s.Filters) > 10 {
		return false
	}
	for k, v := range s.Filters {
		if !hasValue(k, "department", "priority", "channel", "requester", "status", "processing", "sla") || len(v) > 200 {
			return false
		}
	}
	return true
}
func (h *TicketsHandler) ReportSchedules(w http.ResponseWriter, r *http.Request) {
	p, ok := analyticsPrincipal(w, r)
	if !ok {
		return
	}
	tenant := p.TenantID
	if r.Method == "GET" {
		if rawID := r.PathValue("artifact"); rawID != "" {
			id, err := strconv.ParseInt(rawID, 10, 64)
			if err != nil || id <= 0 {
				writeError(w, 400, "Некорректный отчёт")
				return
			}
			var format string
			var content []byte
			if err = h.db.QueryRow(r.Context(), `SELECT format,content FROM report_artifacts WHERE tenant_id=$1 AND id=$2 AND owner_id=$3`, tenant, id, p.ID).Scan(&format, &content); err != nil {
				writeError(w, 404, "Отчёт не найден")
				return
			}
			mime := map[string]string{"csv": "text/csv; charset=utf-8", "xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "pdf": "application/pdf"}[format]
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Content-Disposition", `attachment; filename="flowops-report-`+strconv.FormatInt(id, 10)+`.`+format+`"`)
			w.Header().Set("Cache-Control", "no-store")
			w.Write(content)
			return
		}
		rows, err := h.db.Query(r.Context(), `SELECT id,name,filters,format,period_days,interval_days,active,revision,next_run,last_error FROM report_schedules WHERE tenant_id=$1 AND owner_id=$2 ORDER BY name LIMIT 100`, tenant, p.ID)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить отчёты")
			return
		}
		items := []ReportSchedule{}
		for rows.Next() {
			var s ReportSchedule
			var raw []byte
			if err = rows.Scan(&s.ID, &s.Name, &raw, &s.Format, &s.PeriodDays, &s.IntervalDays, &s.Active, &s.Revision, &s.NextRun, &s.LastError); err != nil {
				break
			}
			if err = json.Unmarshal(raw, &s.Filters); err != nil {
				break
			}
			items = append(items, s)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil || rowErr != nil {
			writeError(w, 503, "Не удалось прочитать отчёты")
			return
		}
		rows, err = h.db.Query(r.Context(), `SELECT a.id,a.schedule_id,s.name,a.period_end,a.created_at,a.format FROM report_artifacts a JOIN report_schedules s ON s.id=a.schedule_id AND s.tenant_id=a.tenant_id WHERE a.tenant_id=$1 AND a.owner_id=$2 ORDER BY a.id DESC LIMIT 100`, tenant, p.ID)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить файлы")
			return
		}
		defer rows.Close()
		files := []map[string]any{}
		for rows.Next() {
			var id int64
			var schedule, name, format string
			var end, created time.Time
			if err = rows.Scan(&id, &schedule, &name, &end, &created, &format); err != nil {
				break
			}
			files = append(files, map[string]any{"id": id, "schedule_id": schedule, "name": name, "period_end": end, "created_at": created, "format": format})
		}
		if err != nil || rows.Err() != nil {
			writeError(w, 503, "Не удалось прочитать файлы")
			return
		}
		writeJSON(w, 200, map[string]any{"schedules": items, "artifacts": files})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/run") {
		tag, err := h.db.Exec(r.Context(), `UPDATE report_schedules SET manual_requested=TRUE WHERE tenant_id=$1 AND id=$2 AND owner_id=$3`, tenant, r.PathValue("id"), p.ID)
		if err != nil || tag.RowsAffected() != 1 {
			writeError(w, 404, "Шаблон не найден")
			return
		}
		writeJSON(w, 202, map[string]any{"queued": true})
		return
	}
	var s ReportSchedule
	if !decodeDialogue(w, r, &s) {
		return
	}
	if !validSchedule(s) {
		writeError(w, 400, "Некорректное расписание")
		return
	}
	cfg, configErr := settings.LoadPlatform(r.Context(), h.db, tenant)
	if configErr != nil {
		writeError(w, 503, "Не удалось проверить фильтры")
		return
	}
	query := url.Values{}
	for k, v := range s.Filters {
		query.Set(k, v)
	}
	filterRequest := r.Clone(r.Context())
	filterURL := *r.URL
	filterURL.RawQuery = query.Encode()
	filterRequest.URL = &filterURL
	if _, filterErr := readAnalyticsFilters(filterRequest, cfg); filterErr != nil {
		writeError(w, 400, filterErr.Error())
		return
	}
	if s.NextRun.IsZero() {
		s.NextRun = time.Now().UTC()
	}
	if s.Active && s.NextRun.Before(time.Now().Add(-time.Minute)) || s.NextRun.After(time.Now().AddDate(1, 0, 0)) {
		writeError(w, 400, "Укажите следующее формирование в течение года")
		return
	}
	raw, _ := json.Marshal(s.Filters)
	if s.Filters == nil {
		raw = []byte("{}")
	}
	tx, err := h.beginTicketTx(r.Context())
	if err != nil {
		writeError(w, 503, "Не удалось сохранить расписание")
		return
	}
	defer tx.Rollback(r.Context())
	if r.Method == "POST" {
		if _, err = tx.Exec(r.Context(), `SELECT tenant_id FROM workspaces WHERE tenant_id=$1 FOR UPDATE`, tenant); err != nil {
			writeError(w, 503, "Не удалось сохранить расписание")
			return
		}
		var count int
		if err = tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM report_schedules WHERE tenant_id=$1 AND owner_id=$2`, tenant, p.ID).Scan(&count); err != nil || count >= 100 {
			writeError(w, 409, "Не более 100 шаблонов")
			return
		}
		s.ID = operationID()
		s.Revision = 1
		_, err = tx.Exec(r.Context(), `INSERT INTO report_schedules(tenant_id,id,owner_id,name,filters,format,period_days,interval_days,active,next_run) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, tenant, s.ID, p.ID, s.Name, raw, s.Format, s.PeriodDays, s.IntervalDays, s.Active, s.NextRun)
	} else {
		if s.ID != r.PathValue("id") {
			writeError(w, 400, "ID не совпадает")
			return
		}
		tag, changeErr := tx.Exec(r.Context(), `UPDATE report_schedules SET name=$4,filters=$5,format=$6,period_days=$7,interval_days=$8,active=$9,next_run=$10,revision=revision+1,last_error='' WHERE tenant_id=$1 AND id=$2 AND owner_id=$3 AND revision=$11`, tenant, s.ID, p.ID, s.Name, raw, s.Format, s.PeriodDays, s.IntervalDays, s.Active, s.NextRun, s.Revision)
		err = changeErr
		if err == nil && tag.RowsAffected() != 1 {
			writeError(w, 409, "Шаблон уже изменён")
			return
		}
		s.Revision++
	}
	if err != nil {
		writeError(w, 503, "Не удалось сохранить расписание")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'REPORT_SCHEDULE_SAVED',$2,$3)`, tenant, p.ID, s.ID); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 503, "Не удалось сохранить расписание")
		return
	}
	writeJSON(w, 200, s)
}

// GenerateScheduledReports uses the same analytical interface and exporters as
// interactive reports. Claims, output and advancement are one transaction.
func (h *TicketsHandler) GenerateScheduledReports(ctx context.Context) error {
	for i := 0; i < 5; i++ {
		worked, err := h.generateReport(ctx)
		if err != nil {
			return err
		}
		if !worked {
			return nil
		}
	}
	return nil
}
func (h *TicketsHandler) generateReport(ctx context.Context) (bool, error) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var s ReportSchedule
	var tenant, owner string
	var raw []byte
	var manual bool
	err = tx.QueryRow(ctx, `SELECT tenant_id,id,owner_id,filters,format,period_days,interval_days,next_run,manual_requested FROM report_schedules WHERE (active AND next_run<=NOW()) OR manual_requested ORDER BY next_run FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&tenant, &s.ID, &owner, &raw, &s.Format, &s.PeriodDays, &s.IntervalDays, &s.NextRun, &manual)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if json.Unmarshal(raw, &s.Filters) != nil {
		return false, errors.New("invalid report filters")
	}
	p := security.Principal{ID: owner, TenantID: tenant, Role: "admin", Owner: owner == "owner"}
	allowed := true
	if owner != "owner" {
		var permissions []byte
		err = tx.QueryRow(ctx, `SELECT m.role,COALESCE(r.permissions,'null'),COALESCE(r.scope,'organization')='organization' FROM workspace_members m LEFT JOIN organization_member_roles a ON a.tenant_id=m.tenant_id AND a.member_id=m.id LEFT JOIN organization_access_roles r ON r.tenant_id=a.tenant_id AND r.id=a.role_id WHERE m.tenant_id=$1 AND m.id=$2 AND m.is_active`, tenant, owner).Scan(&p.Role, &permissions, &allowed)
		if errors.Is(err, pgx.ErrNoRows) {
			allowed = false
		} else if err != nil {
			return false, err
		}
		if string(permissions) != "null" && len(permissions) > 0 {
			var values []string
			if json.Unmarshal(permissions, &values) != nil {
				return false, errors.New("invalid report permissions")
			}
			p.Permissions = &values
		}
		allowed = allowed && hasValue(p.Role, "admin", "analyst") && security.HasPermission(p, "analytics_view") && security.HasPermission(p, "data_export")
	}
	if !allowed {
		_, err = tx.Exec(ctx, `UPDATE report_schedules SET active=FALSE,manual_requested=FALSE,last_error='Доступ владельца отозван' WHERE tenant_id=$1 AND id=$2`, tenant, s.ID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	end := s.NextRun
	if manual {
		end = time.Now().UTC()
	}
	if end.After(time.Now()) {
		end = time.Now().UTC()
	}
	q := url.Values{"from": {end.AddDate(0, 0, -s.PeriodDays).Format(time.RFC3339)}, "to": {end.Format(time.RFC3339)}, "format": {s.Format}}
	for k, v := range s.Filters {
		q.Set(k, v)
	}
	request, _ := http.NewRequestWithContext(security.WithPrincipal(ctx, p), "GET", "http://internal/api/analytics/explore?"+q.Encode(), nil)
	output := httptest.NewRecorder()
	h.Analytics(output, request)
	if output.Code != 200 || output.Body.Len() > 8<<20 {
		_, err = tx.Exec(ctx, `UPDATE report_schedules SET manual_requested=FALSE,last_error='Не удалось сформировать отчёт',next_run=GREATEST(next_run,NOW())+INTERVAL '5 minutes' WHERE tenant_id=$1 AND id=$2`, tenant, s.ID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO report_artifacts(tenant_id,schedule_id,owner_id,period_end,format,content) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, tenant, s.ID, owner, end, s.Format, output.Body.Bytes()); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE report_schedules SET manual_requested=FALSE,last_error='',next_run=CASE WHEN $3 THEN next_run ELSE GREATEST(next_run,NOW())+$4::integer*INTERVAL '1 day' END WHERE tenant_id=$1 AND id=$2`, tenant, s.ID, manual, s.IntervalDays); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'REPORT_GENERATED',$2,$3)`, tenant, owner, s.ID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
