package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flowops-ai/internal/reports"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"github.com/jackc/pgx/v5"
)

type AnalyticsFilters struct {
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Department string    `json:"department"`
	Status     string    `json:"status"`
	Priority   string    `json:"priority"`
	Channel    string    `json:"channel"`
	Requester  string    `json:"requester"`
	Processing string    `json:"processing"`
	SLA        string    `json:"sla"`
}

type AnalyticsMetric struct {
	Value  *float64 `json:"value"`
	Unit   string   `json:"unit"`
	Kind   string   `json:"kind"`
	Sample int      `json:"sample_size"`
	Method string   `json:"method"`
	Source string   `json:"source"`
}
type AnalyticsPoint struct {
	Key    string `json:"key"`
	Total  int    `json:"total"`
	Open   int    `json:"open"`
	Closed int    `json:"closed"`
}
type AnalyticsTicket struct {
	ID         int       `json:"id"`
	Subject    string    `json:"subject"`
	Department string    `json:"department"`
	Status     string    `json:"status"`
	SLA        string    `json:"sla"`
	CreatedAt  time.Time `json:"created_at"`
}
type AnalyticsReport struct {
	Filters      AnalyticsFilters           `json:"filters"`
	Timezone     string                     `json:"timezone"`
	ObservedAt   time.Time                  `json:"observed_at"`
	Metrics      map[string]AnalyticsMetric `json:"metrics"`
	ByDepartment []AnalyticsPoint           `json:"by_department"`
	ByDay        []AnalyticsPoint           `json:"by_day"`
	ByChannel    []AnalyticsPoint           `json:"by_channel"`
	Tickets      []AnalyticsTicket          `json:"tickets,omitempty"`
	NextCursor   int                        `json:"next_cursor,omitempty"`
	Economics    settings.Economics         `json:"economic_assumptions"`
	Limitations  []string                   `json:"limitations"`
}

func readAnalyticsFilters(r *http.Request, c settings.PlatformConfig) (AnalyticsFilters, error) {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return AnalyticsFilters{}, err
	}
	q := r.URL.Query()
	now := time.Now().In(loc)
	f := AnalyticsFilters{From: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -29), To: time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc), Department: q.Get("department"), Status: q.Get("status"), Priority: q.Get("priority"), Channel: q.Get("channel"), Requester: q.Get("requester"), Processing: q.Get("processing"), SLA: q.Get("sla")}
	for _, item := range []struct {
		key    string
		target *time.Time
		end    bool
	}{{"from", &f.From, false}, {"to", &f.To, true}} {
		if raw := q.Get(item.key); raw != "" {
			if len(raw) == 10 {
				*item.target, err = time.ParseInLocation("2006-01-02", raw, loc)
				if item.end && err == nil {
					*item.target = item.target.AddDate(0, 0, 1)
				}
			} else {
				*item.target, err = time.Parse(time.RFC3339, raw)
			}
			if err != nil {
				return f, fmt.Errorf("Некорректная дата %s", item.key)
			}
		}
	}
	if !f.To.After(f.From) || f.To.Sub(f.From) > 367*24*time.Hour {
		return f, fmt.Errorf("Выберите период до одного года")
	}
	for _, v := range []string{f.Department, f.Status, f.Priority, f.Channel, f.Requester, f.Processing, f.SLA} {
		if len(v) > 128 {
			return f, fmt.Errorf("Слишком длинный фильтр")
		}
	}
	if f.Status != "" && !hasValue(f.Status, "new", "in_progress", "closed") {
		return f, fmt.Errorf("Некорректный статус")
	}
	if f.Processing != "" && !hasValue(f.Processing, "manual", "automatic", "assisted", "pending") {
		return f, fmt.Errorf("Некорректный способ обработки")
	}
	if f.SLA != "" && !hasValue(f.SLA, "met", "breached", "unconfigured") {
		return f, fmt.Errorf("Некорректный фильтр SLA")
	}
	if tenant := q.Get("organization"); tenant != "" && tenant != security.TenantID(r.Context()) {
		return f, fmt.Errorf("Другая организация недоступна")
	}
	return f, nil
}

const analyticsBaseSQL = `WITH cohort AS (
 SELECT t.*, ` + ticketDisplayStatusSQL + ` display_status,
 (SELECT MIN(m.created_at) FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND m.role IN ('operator','assistant') AND m.source_key NOT IN ('legacy-reply','initial-answer')) first_response_at,
 COALESCE((SELECT MAX(e.occurred_at) FROM ticket_events e WHERE e.tenant_id=t.tenant_id AND e.ticket_id=t.id AND e.event_type='closed'),(SELECT MAX(a.created_at) FROM audit_logs a WHERE a.tenant_id=t.tenant_id AND a.ticket_id=t.id AND a.event_type='TICKET_CLOSED' AND a.created_at>=t.created_at)) closed_at,
 (SELECT COUNT(*) FROM ticket_events e WHERE e.tenant_id=t.tenant_id AND e.ticket_id=t.id AND e.event_type='reopened') reopens,
 (SELECT COUNT(*) FROM audit_logs a WHERE a.tenant_id=t.tenant_id AND a.ticket_id=t.id AND a.event_type='TICKET_ROUTING_CORRECTED' AND a.created_at>=t.created_at) redirects,
 COALESCE((SELECT SUM(w.active_seconds) FROM ticket_work_sessions w WHERE w.tenant_id=t.tenant_id AND w.ticket_id=t.id),0) work_seconds,
 (SELECT COUNT(*) FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND m.role='operator' AND m.source_key<>'legacy-reply') operator_messages,
 EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=t.tenant_id AND m.ticket_id=t.id AND m.role='employee' AND m.source_key<>'initial' AND m.created_at>(SELECT MIN(x.created_at) FROM ticket_messages x WHERE x.tenant_id=t.tenant_id AND x.ticket_id=t.id AND x.role='operator')) followup_after_reply
 FROM tickets t WHERE t.tenant_id=$1 AND t.created_at >= $2::timestamptz AT TIME ZONE 'UTC' AND t.created_at < $3::timestamptz AT TIME ZONE 'UTC'
 AND ($4='' OR t.department=$4) AND ($5='' OR t.priority=$5) AND ($6='' OR t.channel=$6) AND ($7='' OR t.requester_id=$7)
), classified AS (
 SELECT *,CASE WHEN display_status='new' AND NULLIF(BTRIM(rag_answer),'') IS NULL THEN 'pending'
 WHEN display_status<>'new' AND (NULLIF(BTRIM(operator_reply),'') IS NOT NULL OR work_seconds>0 OR operator_messages>0) THEN CASE WHEN NULLIF(BTRIM(rag_answer),'') IS NOT NULL THEN 'assisted' ELSE 'manual' END
 WHEN NULLIF(BTRIM(rag_answer),'') IS NOT NULL THEN 'automatic' ELSE 'manual' END processing,
 CASE WHEN first_response_due_at IS NULL AND resolution_due_at IS NULL THEN 'unconfigured'
 WHEN (first_response_due_at IS NOT NULL AND COALESCE(first_response_at,closed_at,NOW())>first_response_due_at)
 OR (resolution_due_at IS NOT NULL AND (CASE WHEN display_status='closed' THEN COALESCE(closed_at,NOW()) ELSE NOW() END)>resolution_due_at) THEN 'breached' ELSE 'met' END sla_result
 FROM cohort
), filtered AS (SELECT * FROM classified WHERE ($8='' OR display_status=$8) AND ($9='' OR processing=$9) AND ($10='' OR sla_result=$10)) `

func analyticsPrincipal(w http.ResponseWriter, r *http.Request) (security.Principal, bool) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || (p.Role != "admin" && p.Role != "analyst") {
		writeError(w, 403, "Нет доступа к аналитике")
		return p, false
	}
	return p, true
}

func (h *TicketsHandler) Analytics(w http.ResponseWriter, r *http.Request) {
	p, ok := analyticsPrincipal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	cfg, err := settings.LoadPlatform(ctx, h.db, security.TenantID(ctx))
	if err != nil {
		writeError(w, 503, "Не удалось загрузить настройки расчёта")
		return
	}
	f, err := readAnalyticsFilters(r, cfg)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	args := []any{security.TenantID(ctx), f.From, f.To, f.Department, f.Priority, f.Channel, f.Requester, f.Status, f.Processing, f.SLA}
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeError(w, 503, "Аналитика недоступна")
		return
	}
	defer tx.Rollback(ctx)
	// Bound execution in PostgreSQL as well as the HTTP context.
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='15s'`); err != nil {
		writeError(w, 503, "Аналитика недоступна")
		return
	}
	var total, newCount, open, closed, overdue, responseN, resolutionN, reopenCount, redirectCount, measuredN, autoCount, aiCount, ratingN, slaN, slaMet, fcrN, fcrEligible int
	var avgResponse, medianResponse, avgResolution, medianResolution *float64
	var workSeconds, ratingAverage float64
	query := analyticsBaseSQL + `SELECT COUNT(*),COUNT(*) FILTER(WHERE display_status='new'),COUNT(*) FILTER(WHERE display_status<>'closed'),COUNT(*) FILTER(WHERE display_status='closed'),COUNT(*) FILTER(WHERE display_status<>'closed' AND sla_result='breached'),
 AVG(EXTRACT(EPOCH FROM first_response_at-(created_at AT TIME ZONE 'UTC'))) FILTER(WHERE first_response_at>=created_at AT TIME ZONE 'UTC'),
 PERCENTILE_CONT(0.5) WITHIN GROUP(ORDER BY EXTRACT(EPOCH FROM first_response_at-(created_at AT TIME ZONE 'UTC'))) FILTER(WHERE first_response_at>=created_at AT TIME ZONE 'UTC'),COUNT(*) FILTER(WHERE first_response_at>=created_at AT TIME ZONE 'UTC'),
 AVG(EXTRACT(EPOCH FROM closed_at-(created_at AT TIME ZONE 'UTC'))) FILTER(WHERE display_status='closed' AND closed_at>=created_at AT TIME ZONE 'UTC'),
 PERCENTILE_CONT(0.5) WITHIN GROUP(ORDER BY EXTRACT(EPOCH FROM closed_at-(created_at AT TIME ZONE 'UTC'))) FILTER(WHERE display_status='closed' AND closed_at>=created_at AT TIME ZONE 'UTC'),COUNT(*) FILTER(WHERE display_status='closed' AND closed_at>=created_at AT TIME ZONE 'UTC'),
 COALESCE(SUM(reopens),0),COALESCE(SUM(redirects),0),COALESCE(SUM(work_seconds),0),COUNT(*) FILTER(WHERE work_seconds>0),
 COUNT(*) FILTER(WHERE status='auto_resolved' AND NULLIF(BTRIM(rag_answer),'') IS NOT NULL AND NULLIF(BTRIM(operator_reply),'') IS NULL AND operator_messages=0 AND work_seconds=0),
 COUNT(*) FILTER(WHERE NULLIF(BTRIM(rag_answer),'') IS NOT NULL),COUNT(*) FILTER(WHERE sla_result<>'unconfigured'),COUNT(*) FILTER(WHERE sla_result='met'),
 COUNT(*) FILTER(WHERE display_status='closed' AND operator_messages=1 AND reopens=0 AND NOT followup_after_reply),COUNT(*) FILTER(WHERE display_status='closed' AND operator_messages>0)
 FROM filtered`
	err = tx.QueryRow(ctx, query, args...).Scan(&total, &newCount, &open, &closed, &overdue, &avgResponse, &medianResponse, &responseN, &avgResolution, &medianResolution, &resolutionN, &reopenCount, &redirectCount, &workSeconds, &measuredN, &autoCount, &aiCount, &slaN, &slaMet, &fcrN, &fcrEligible)
	if err != nil {
		h.logger.Error("analytics aggregate failed", "error", err.Error())
		writeError(w, 503, "Не удалось рассчитать показатели")
		return
	}
	err = tx.QueryRow(ctx, analyticsBaseSQL+`SELECT COUNT(*),COALESCE(AVG(r.score),0) FROM ticket_answer_ratings r JOIN ticket_messages m ON m.id=r.message_id JOIN filtered t ON t.id=m.ticket_id AND t.tenant_id=m.tenant_id`, args...).Scan(&ratingN, &ratingAverage)
	if err != nil {
		writeError(w, 503, "Не удалось рассчитать обратную связь")
		return
	}
	report := AnalyticsReport{Filters: f, Timezone: cfg.Timezone, ObservedAt: time.Now().UTC(), Metrics: map[string]AnalyticsMetric{}, Economics: cfg.Economics,
		Limitations: []string{"Выборка: обращения, созданные в выбранном периоде; показатели отражают их текущее состояние.", "Исторические сроки SLA и время ответа без достоверного события не восстанавливаются. Импортированные старые ответы исключены из времени первого ответа.", "Календарная длительность и активное рабочее время показаны отдельно. Начатый незавершённый интервал работы не прибавляется к сохранённому времени.", "Стоимость времени и экономический эффект расчётные; стоимость ИИ не известна без подтверждённого биллинга.", "Рекомендации и регламенты, не использованные при ответе, не считаются обработкой ИИ."}}
	add := func(key string, value *float64, unit, kind string, n int, method, source string) {
		report.Metrics[key] = AnalyticsMetric{value, unit, kind, n, method, source}
	}
	value := func(n float64) *float64 { return &n }
	for key, n := range map[string]int{"total": total, "new": newCount, "active": open, "closed": closed, "overdue": overdue, "reopens": reopenCount, "redirects": redirectCount} {
		add(key, value(float64(n)), "count", "measured", total, "Количество записей, соответствующих фильтрам", "tickets / ticket_events / audit_logs")
	}
	add("first_response_mean", avgResponse, "seconds", "measured", responseN, "Среднее время от создания до первого записанного ответа специалиста/помощника; без импортированных ответов", "ticket_messages")
	add("first_response_median", medianResponse, "seconds", "measured", responseN, "Медиана времени первого записанного ответа", "ticket_messages")
	add("resolution_mean", avgResolution, "seconds", "measured", resolutionN, "Среднее время от создания до последнего зафиксированного закрытия", "ticket_events / audit_logs")
	add("resolution_median", medianResolution, "seconds", "measured", resolutionN, "Медиана календарного времени решения", "ticket_events / audit_logs")
	add("active_hours", value(workSeconds/3600), "hours", "measured", measuredN, "Сумма сохранённых активных секунд операторов / 3600", "ticket_work_sessions")
	percent := func(key string, n, d int, method, source string) {
		var v *float64
		if d > 0 {
			v = value(float64(n) * 100 / float64(d))
		}
		add(key, v, "percent", "calculated", d, method, source)
	}
	percent("deflection", autoCount, total, "Подтверждённые автоматические решения без участия оператора / все обращения × 100", "tickets / ticket_messages / ticket_work_sessions")
	percent("ai_share", aiCount, total, "Обращения с сохранённой инструкцией ИИ / все обращения × 100; не эквивалентно числу вызовов модели", "tickets.rag_answer")
	percent("sla_compliance", slaMet, slaN, "Обращения с настроенным SLA без зафиксированного или текущего нарушения / все обращения с SLA × 100; активные обращения могут нарушить SLA позднее", "snapshot SLA deadlines / recorded responses / closure events")
	percent("first_contact_resolution", fcrN, fcrEligible, "Закрытые обращения с одним записанным ответом оператора, без последующего сообщения заявителя и переоткрытия / закрытые обращения с записанными ответами оператора × 100", "ticket_messages / ticket_events")
	var rating *float64
	if ratingN > 0 {
		rating = value(ratingAverage)
	}
	add("satisfaction", rating, "score_1_5", "measured", ratingN, "Средняя оценка ответов, взвешенная по числу оценок", "ticket_answer_ratings")
	savedHours := float64(autoCount) * cfg.Economics.BaselineMinutes / 60
	add("released_hours", value(savedHours), "hours", "estimate", autoCount, "Подтверждённые автоматические решения × базовые минуты ручной обработки / 60", "organization.economics / confirmed tickets")
	add("released_time_value", value(savedHours*cfg.Economics.HourlyRate), cfg.Economics.Currency, "estimate", autoCount, "Высвобождённые часы × ставка; не гарантированное сокращение денежных расходов", "organization.economics")
	add("ai_cost", nil, cfg.Economics.Currency, "unavailable", 0, "Не рассчитывается без подтверждённых данных провайдера о расходах", "provider billing unavailable")
	add("cost_per_ticket", nil, cfg.Economics.Currency, "unavailable", 0, "Полная стоимость неизвестна без расходов ИИ и подтверждённых распределений затрат", "billing unavailable")
	add("routing_accuracy", nil, "percent", "unavailable", 0, "Нет подтверждённой разметки правильных первичных маршрутов; перенаправление не считается доказанной ошибкой", "verified routing labels unavailable")
	for _, dimension := range []struct {
		expr   string
		target *[]AnalyticsPoint
	}{{"COALESCE(NULLIF(department,''),'Unassigned')", &report.ByDepartment}, {"to_char(created_at AT TIME ZONE 'UTC' AT TIME ZONE $11,'YYYY-MM-DD')", &report.ByDay}, {"COALESCE(channel,'web')", &report.ByChannel}} {
		a := append([]any(nil), args...)
		if dimension.target == &report.ByDay {
			a = append(a, cfg.Timezone)
		}
		rows, e := tx.Query(ctx, analyticsBaseSQL+`SELECT `+dimension.expr+`,COUNT(*),COUNT(*) FILTER(WHERE display_status<>'closed'),COUNT(*) FILTER(WHERE display_status='closed') FROM filtered GROUP BY 1 ORDER BY 1`, a...)
		if e != nil {
			writeError(w, 503, "Не удалось сгруппировать аналитику")
			return
		}
		*dimension.target = []AnalyticsPoint{}
		for rows.Next() {
			var point AnalyticsPoint
			if err = rows.Scan(&point.Key, &point.Total, &point.Open, &point.Closed); err != nil {
				break
			}
			*dimension.target = append(*dimension.target, point)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil || rowErr != nil {
			writeError(w, 503, "Не удалось прочитать аналитику")
			return
		}
	}
	if p.Role == "admin" {
		cursor, _ := strconv.Atoi(r.URL.Query().Get("before_id"))
		if cursor < 0 {
			cursor = 0
		}
		rows, e := tx.Query(ctx, analyticsBaseSQL+`SELECT id,COALESCE(NULLIF(subject,''),extracted_summary,''),COALESCE(department,''),display_status,sla_result,created_at FROM filtered WHERE ($11::integer=0 OR id<$11) ORDER BY id DESC LIMIT 51`, append(args, cursor)...)
		if e != nil {
			writeError(w, 503, "Не удалось загрузить детализацию")
			return
		}
		report.Tickets = []AnalyticsTicket{}
		for rows.Next() {
			var ticket AnalyticsTicket
			if err = rows.Scan(&ticket.ID, &ticket.Subject, &ticket.Department, &ticket.Status, &ticket.SLA, &ticket.CreatedAt); err != nil {
				break
			}
			report.Tickets = append(report.Tickets, ticket)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil || rowErr != nil {
			writeError(w, 503, "Не удалось прочитать детализацию")
			return
		}
		if len(report.Tickets) > 50 {
			report.Tickets = report.Tickets[:50]
			report.NextCursor = report.Tickets[49].ID
		}
	}
	rates, _ := json.Marshal(cfg.Economics.DepartmentRates)
	costArgs := append(append([]any(nil), args...), cfg.Economics.BaselineMinutes, string(rates), cfg.Economics.HourlyRate)
	var releasedValue, laborValue float64
	err = tx.QueryRow(ctx, analyticsBaseSQL+`SELECT COALESCE(SUM(CASE WHEN status='auto_resolved' AND NULLIF(BTRIM(rag_answer),'') IS NOT NULL AND NULLIF(BTRIM(operator_reply),'') IS NULL AND operator_messages=0 AND work_seconds=0 THEN $11::numeric/60*COALESCE(($12::jsonb->>department)::numeric,$13::numeric) ELSE 0 END),0),COALESCE(SUM(work_seconds/3600*COALESCE(($12::jsonb->>department)::numeric,$13::numeric)),0) FROM filtered`, costArgs...).Scan(&releasedValue, &laborValue)
	if err != nil {
		writeError(w, 503, "Не удалось рассчитать стоимость времени")
		return
	}
	add("released_time_value", value(releasedValue), cfg.Economics.Currency, "estimate", autoCount, "Подтверждённые автоматические решения × базовое время × ставка подразделения (либо общая ставка). Не гарантирует сокращение расходов.", "organization.economics / confirmed tickets")
	add("measured_labor_value", value(laborValue), cfg.Economics.Currency, "estimate", measuredN, "Сохранённое активное рабочее время × заданная ставка подразделения. Ставка — допущение, не начисленная зарплата.", "ticket_work_sessions / organization.economics")
	allocated := monthlyAllocation(f.From, f.To, cfg.Economics.MonthlyInfrastructure+cfg.Economics.MonthlyMaintenance)
	add("allocated_operating_cost", value(allocated), cfg.Economics.Currency, "estimate", total, "Месячная инфраструктура и сопровождение пропорционально календарной доле каждого месяца периода.", "organization.economics")
	add("partial_effect", value(releasedValue-allocated), cfg.Economics.Currency, "estimate", total, "Оценка стоимости высвобождённого времени минус распределённая инфраструктура и сопровождение. Расходы ИИ и внедрение не вычтены; это частичная оценка.", "organization.economics / confirmed tickets")
	add("roi", nil, "percent", "unavailable", 0, "Не рассчитывается как полный ROI без подтверждённых затрат и денежного эффекта.", "complete financial data unavailable")
	add("payback_months", nil, "months", "unavailable", 0, "Не рассчитывается без подтверждённого денежного эффекта и полных затрат.", "complete financial data unavailable")
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Не удалось завершить расчёт")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if format := r.URL.Query().Get("format"); format == "xlsx" || format == "pdf" {
		table := analyticsTable(report)
		var raw []byte
		var err error
		if format == "xlsx" {
			raw, err = reports.XLSX(table)
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		} else {
			raw, err = reports.PDF(table)
			w.Header().Set("Content-Type", "application/pdf")
		}
		if err != nil {
			writeError(w, 500, "Не удалось сформировать отчёт")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="flowops-analytics.`+format+`"`)
		w.Write(raw)
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		writeAnalyticsCSV(w, report)
		return
	}
	writeJSON(w, 200, report)
}

func csvSafe(s string) string {
	if len(s) > 0 && strings.ContainsAny(s[:1], "=+-@\t\r\n") {
		return "'" + s
	}
	return s
}
func writeAnalyticsCSV(w http.ResponseWriter, report AnalyticsReport) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="flowops-analytics.csv"`)
	w.Write([]byte{0xef, 0xbb, 0xbf})
	out := csv.NewWriter(w)
	out.Write([]string{"metric", "value", "unit", "kind", "sample_size", "method", "source", "from", "to", "timezone"})
	for key, metric := range report.Metrics {
		v := ""
		if metric.Value != nil {
			v = strconv.FormatFloat(*metric.Value, 'f', 4, 64)
		}
		out.Write([]string{csvSafe(key), csvSafe(v), csvSafe(metric.Unit), metric.Kind, strconv.Itoa(metric.Sample), csvSafe(metric.Method), csvSafe(metric.Source), report.Filters.From.Format(time.RFC3339), report.Filters.To.Format(time.RFC3339), report.Timezone})
	}
	out.Flush()
}

func analyticsTable(report AnalyticsReport) reports.Table {
	table := reports.Table{Title: "Аналитика FlowOps AI", Subtitle: report.Filters.From.Format(time.RFC3339) + " — " + report.Filters.To.Format(time.RFC3339) + " · " + report.Timezone, Headers: []string{"Показатель", "Значение", "Единица", "Тип", "Размер выборки", "Методика", "Источник"}, Notes: report.Limitations}
	keys := []string{}
	for key := range report.Metrics {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	labels := map[string]string{"total": "Все обращения", "new": "Новые", "active": "Активные", "closed": "Закрытые", "overdue": "Просроченные", "first_response_mean": "Среднее время первого ответа", "first_response_median": "Медиана первого ответа", "resolution_mean": "Среднее время решения", "resolution_median": "Медиана решения", "active_hours": "Измеренные рабочие часы", "released_hours": "Расчётные высвобожденные часы", "released_time_value": "Расчётная стоимость времени", "reopens": "Переоткрытия", "redirects": "Перенаправления", "satisfaction": "Оценка ответов", "sla_compliance": "SLA Compliance", "routing_accuracy": "Точность маршрутизации", "ai_cost": "Расходы ИИ", "cost_per_ticket": "Стоимость обращения", "ai_share": "Доля инструкций ИИ", "deflection": "Deflection Rate", "first_contact_resolution": "First Contact Resolution"}
	for _, key := range keys {
		m := report.Metrics[key]
		value := "Нет данных"
		if m.Value != nil {
			value = strconv.FormatFloat(*m.Value, 'f', 2, 64)
		}
		label := labels[key]
		if label == "" {
			label = key
		}
		table.Rows = append(table.Rows, []string{label, value, m.Unit, m.Kind, strconv.Itoa(m.Sample), m.Method, m.Source})
	}
	return table
}

func monthlyAllocation(from, to time.Time, monthly float64) float64 {
	sum := float64(0)
	cursor := from
	for cursor.Before(to) {
		start := time.Date(cursor.Year(), cursor.Month(), 1, 0, 0, 0, 0, cursor.Location())
		end := start.AddDate(0, 1, 0)
		stop := end
		if stop.After(to) {
			stop = to
		}
		sum += monthly * stop.Sub(cursor).Seconds() / end.Sub(start).Seconds()
		cursor = stop
	}
	return sum
}
