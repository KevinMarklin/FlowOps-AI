package handlers

import (
	"context"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

const processTimelineSQL = `, timeline AS (
 SELECT e.ticket_id,e.id,e.occurred_at,CASE WHEN e.event_type='created' THEN e.payload->>'status' ELSE e.payload->>'to' END stage,
 LEAD(e.occurred_at,1,LEAST(NOW(),$3::timestamptz)) OVER(PARTITION BY e.ticket_id ORDER BY e.occurred_at,e.id) until_at
 FROM ticket_events e JOIN filtered t ON t.id=e.ticket_id AND t.tenant_id=e.tenant_id
 WHERE e.event_type IN ('created','status_changed') AND e.origin='observed' AND e.occurred_at<$3::timestamptz
), intervals AS(SELECT *,GREATEST(0,EXTRACT(EPOCH FROM until_at-occurred_at)) seconds FROM timeline WHERE stage NOT IN ('closed','auto_resolved')) `

func (h *TicketsHandler) ProcessAnalytics(w http.ResponseWriter, r *http.Request) {
	p, ok := analyticsPrincipal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	cfg, err := settings.LoadPlatform(ctx, h.db, p.TenantID)
	if err != nil {
		writeError(w, 503, "Не удалось загрузить настройки")
		return
	}
	f, err := readAnalyticsFilters(r, cfg)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	args := []any{p.TenantID, f.From, f.To, f.Department, f.Priority, f.Channel, f.Requester, f.Status, f.Processing, f.SLA}
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeError(w, 503, "Аналитика недоступна")
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='15s'`); err != nil {
		writeError(w, 503, "Аналитика недоступна")
		return
	}
	stages := []map[string]any{}
	rows, err := tx.Query(ctx, analyticsBaseSQL+processTimelineSQL+`SELECT stage,COUNT(*),COUNT(DISTINCT ticket_id),AVG(seconds),PERCENTILE_CONT(0.5) WITHIN GROUP(ORDER BY seconds),COALESCE(SUM(seconds) FILTER(WHERE stage IN ('awaiting_confirmation','waiting_employee')),0) FROM intervals GROUP BY stage ORDER BY AVG(seconds) DESC`, args...)
	if err != nil {
		writeError(w, 503, "Не удалось рассчитать этапы")
		return
	}
	for rows.Next() {
		var stage string
		var n, tickets int
		var average, median, waiting float64
		if err = rows.Scan(&stage, &n, &tickets, &average, &median, &waiting); err != nil {
			break
		}
		stages = append(stages, map[string]any{"stage": stage, "intervals": n, "tickets": tickets, "mean_seconds": average, "median_seconds": median, "waiting_seconds": waiting})
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		writeError(w, 503, "Не удалось прочитать этапы")
		return
	}
	const routeSQL = `, routes AS(SELECT e.ticket_id,e.id,e.occurred_at,CASE WHEN e.event_type='created' THEN e.payload->>'department' ELSE e.payload->>'to' END department FROM ticket_events e JOIN filtered t ON t.id=e.ticket_id AND t.tenant_id=e.tenant_id WHERE e.event_type IN ('created','department_changed') AND e.origin='observed' AND e.occurred_at<$3::timestamptz), variants AS(SELECT ticket_id,string_agg(COALESCE(department,'?'),' → ' ORDER BY occurred_at,id) path,COUNT(*)>COUNT(DISTINCT department) has_loop FROM routes GROUP BY ticket_id) `
	routes := []map[string]any{}
	rows, err = tx.Query(ctx, analyticsBaseSQL+routeSQL+`SELECT v.path,COUNT(*),COUNT(*) FILTER(WHERE v.has_loop),AVG(GREATEST(0,EXTRACT(EPOCH FROM LEAST(COALESCE(t.closed_at,NOW()),$3::timestamptz)-(t.created_at AT TIME ZONE 'UTC')))),AVG(t.work_seconds) FROM variants v JOIN filtered t ON t.id=v.ticket_id GROUP BY v.path ORDER BY COUNT(*) DESC,v.path LIMIT 30`, args...)
	if err != nil {
		writeError(w, 503, "Не удалось рассчитать маршруты")
		return
	}
	for rows.Next() {
		var path string
		var n, loops int
		var elapsed, work float64
		if err = rows.Scan(&path, &n, &loops, &elapsed, &work); err != nil {
			break
		}
		routes = append(routes, map[string]any{"path": path, "tickets": n, "loops": loops, "calendar_mean_seconds": elapsed, "measured_work_mean_seconds": work})
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		writeError(w, 503, "Не удалось прочитать маршруты")
		return
	}
	var total, observed, loops int
	var waiting, work float64
	if err = tx.QueryRow(ctx, analyticsBaseSQL+processTimelineSQL+`SELECT (SELECT COUNT(*) FROM filtered),(SELECT COUNT(*) FROM filtered t WHERE EXISTS(SELECT 1 FROM ticket_events e WHERE e.ticket_id=t.id AND e.tenant_id=t.tenant_id AND e.event_type='created' AND e.origin='observed')),COALESCE((SELECT SUM(seconds) FROM intervals WHERE stage IN ('awaiting_confirmation','waiting_employee')),0),(SELECT COALESCE(SUM(work_seconds),0) FROM filtered)`, args...).Scan(&total, &observed, &waiting, &work); err != nil {
		writeError(w, 503, "Не удалось рассчитать покрытие")
		return
	}
	if err = tx.QueryRow(ctx, analyticsBaseSQL+routeSQL+`SELECT COUNT(*) FROM variants WHERE has_loop`, args...).Scan(&loops); err != nil {
		writeError(w, 503, "Не удалось рассчитать циклы")
		return
	}
	workflowStages := []map[string]any{}
	rows, err = tx.Query(ctx, analyticsBaseSQL+`, wf_intervals AS(SELECT r.ticket_id,n->>'kind' kind,s.state,GREATEST(0,EXTRACT(EPOCH FROM LEAST(COALESCE(s.finished_at,NOW()),$3::timestamptz)-s.started_at)) seconds FROM workflow_steps s JOIN workflow_runs r ON r.id=s.run_id JOIN filtered t ON t.id=r.ticket_id AND t.tenant_id=r.tenant_id CROSS JOIN jsonb_array_elements(r.definition->'nodes') n WHERE n->>'id'=s.node_id AND n->>'kind' NOT IN ('start','end') AND s.started_at IS NOT NULL AND s.started_at<$3::timestamptz) SELECT kind,COUNT(*),COUNT(DISTINCT ticket_id),AVG(seconds),PERCENTILE_CONT(0.5) WITHIN GROUP(ORDER BY seconds),COUNT(*) FILTER(WHERE state IN ('pending','waiting')) FROM wf_intervals GROUP BY kind ORDER BY AVG(seconds) DESC`, args...)
	if err != nil {
		writeError(w, 503, "Не удалось рассчитать этапы процессов")
		return
	}
	for rows.Next() {
		var kind string
		var n, tickets, open int
		var average, median float64
		if err = rows.Scan(&kind, &n, &tickets, &average, &median, &open); err != nil {
			break
		}
		workflowStages = append(workflowStages, map[string]any{"kind": kind, "intervals": n, "tickets": tickets, "mean_seconds": average, "median_seconds": median, "open_intervals": open})
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		writeError(w, 503, "Не удалось прочитать этапы процессов")
		return
	}
	comparison := []map[string]any{}
	if value := r.URL.Query().Get("change_at"); value != "" {
		at, parseErr := time.Parse(time.RFC3339, value)
		if parseErr != nil || !at.After(f.From) || !at.Before(f.To) {
			writeError(w, 400, "Дата изменения должна находиться внутри периода")
			return
		}
		rows, err = tx.Query(ctx, analyticsBaseSQL+`SELECT CASE WHEN created_at AT TIME ZONE 'UTC'<$11::timestamptz THEN 'before' ELSE 'after' END period,COUNT(*),AVG(EXTRACT(EPOCH FROM closed_at-(created_at AT TIME ZONE 'UTC'))) FILTER(WHERE display_status='closed'),AVG(work_seconds),COUNT(*) FILTER(WHERE sla_result='breached') FROM filtered GROUP BY 1`, append(args, at)...)
		if err != nil {
			writeError(w, 503, "Не удалось сравнить периоды")
			return
		}
		for rows.Next() {
			var period string
			var n, breaches int
			var elapsed *float64
			var active float64
			if err = rows.Scan(&period, &n, &elapsed, &active, &breaches); err != nil {
				break
			}
			comparison = append(comparison, map[string]any{"period": period, "tickets": n, "closed_calendar_mean_seconds": elapsed, "measured_work_mean_seconds": active, "sla_breaches": breaches})
		}
		rowErr = rows.Err()
		rows.Close()
		if err != nil || rowErr != nil {
			writeError(w, 503, "Не удалось прочитать сравнение")
			return
		}
	}
	writeJSON(w, 200, map[string]any{"filters": f, "timezone": cfg.Timezone, "workflow_stages": workflowStages, "stages": stages, "routes": routes, "comparison": comparison, "tickets": total, "observed_creation_tickets": observed, "loop_tickets": loops, "waiting_seconds": waiting, "measured_work_seconds": work, "limitations": []string{"Этапы и маршруты построены только по записанным событиям; пропущенная история не восстановлена догадками.", "Время этапа — календарное; измеренное рабочее время учитывается отдельно. Последний открытый этап ограничен концом периода или текущим временем.", "Сравнение до и после — наблюдение разных выборок, не доказательство причинного эффекта автоматизации.", "Показаны 30 наиболее частых вариантов маршрута.", "Длительности параллельных шагов workflow могут перекрываться; их нельзя суммировать как общее время процесса."}})
}
