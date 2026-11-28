package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flowops-ai/internal/security"
	"flowops-ai/internal/settings"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

type OptimizationTask struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Assignee string `json:"assignee_id"`
}
type Opportunity struct {
	Currency       string             `json:"currency"`
	ID             string             `json:"id"`
	Kind           string             `json:"kind"`
	Title          string             `json:"title"`
	Description    string             `json:"description"`
	Department     string             `json:"department"`
	Count          int                `json:"ticket_count"`
	TicketIDs      []int              `json:"ticket_ids,omitempty"`
	WorkingHours   float64            `json:"working_hours"`
	PotentialHours *float64           `json:"potential_hours"`
	PotentialValue *float64           `json:"potential_value"`
	Method         string             `json:"method"`
	Confidence     string             `json:"confidence"`
	State          string             `json:"state"`
	Assignee       string             `json:"assignee_id"`
	Note           string             `json:"note"`
	Revision       int64              `json:"revision"`
	Tasks          []OptimizationTask `json:"tasks"`
	BaselineCount  int                `json:"baseline_count,omitempty"`
	Current        bool               `json:"currently_detected"`
	From           time.Time          `json:"from"`
	To             time.Time          `json:"to"`
}

func opportunityID(tenant, kind, department, key string) string {
	sum := sha256.Sum256([]byte(tenant + "\x00" + kind + "\x00" + department + "\x00" + key))
	return hex.EncodeToString(sum[:16])
}
func (h *TicketsHandler) discoverOpportunities(ctx context.Context, r *http.Request, c settings.PlatformConfig) ([]Opportunity, error) {
	f, err := readAnalyticsFilters(r, c)
	if err != nil {
		return nil, err
	}
	args := []any{security.TenantID(ctx), f.From, f.To, f.Department, f.Priority, f.Channel, f.Requester, f.Status, f.Processing, f.SLA}
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='15s'`); err != nil {
		return nil, err
	}
	result := []Opportunity{}
	queries := []struct{ kind, title, description, sql, confidence string }{
		{"exact_repeats", "Повторяющиеся обращения", "Проверьте повторяющийся сценарий и возможность единообразной инструкции.", `SELECT COALESCE(department,'Unassigned'),lower(regexp_replace(BTRIM(raw_text),'\s+',' ','g')),COUNT(*),(array_agg(id ORDER BY id DESC))[1:20],COALESCE(SUM(work_seconds),0) FROM filtered GROUP BY 1,2 HAVING COUNT(*)>=3 ORDER BY COUNT(*) DESC LIMIT 50`, "Точное совпадение нормализованного текста; не семантический кластер"},
		{"knowledge_gap", "Не хватает регламентов", "Несколько обращений закрыты специалистами без сохранённой инструкции. Подготовьте черновик и отправьте на согласование.", `SELECT COALESCE(department,'Unassigned'),'department',COUNT(*),(array_agg(id ORDER BY id DESC))[1:20],COALESCE(SUM(work_seconds),0) FROM filtered WHERE display_status='closed' AND NULLIF(BTRIM(rag_answer),'') IS NULL AND NULLIF(BTRIM(operator_reply),'') IS NOT NULL GROUP BY 1 HAVING COUNT(*)>=3 ORDER BY COUNT(*) DESC LIMIT 50`, "Прямые данные обращений; отсутствие инструкции в ответе не доказывает отсутствие подходящей статьи"},
		{"sla_breach", "Нарушения SLA", "Проверьте просроченные этапы и нагрузку ответственного подразделения.", `SELECT COALESCE(department,'Unassigned'),'department',COUNT(*),(array_agg(id ORDER BY id DESC))[1:20],COALESCE(SUM(work_seconds),0) FROM filtered WHERE sla_result='breached' GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 50`, "Настроенные сроки SLA, зафиксированные ответы и закрытия"},
		{"routing", "Частые перенаправления", "Проверьте правила назначения; перенаправление само по себе не считается ошибкой ИИ.", `SELECT COALESCE(department,'Unassigned'),'department',COUNT(*),(array_agg(id ORDER BY id DESC))[1:20],COALESCE(SUM(work_seconds),0) FROM filtered WHERE redirects>=2 GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 50`, "Не менее двух записанных перенаправлений на обращение"},
		{"routing_loop", "Циклические маршруты", "Обращения возвращаются в уже посещённый отдел. Сопоставьте фактические маршруты и условия передачи в аналитике процессов.", `SELECT COALESCE(department,'Unassigned'),'department',COUNT(*),(array_agg(id ORDER BY id DESC))[1:20],COALESCE(SUM(work_seconds),0) FROM filtered t WHERE EXISTS(SELECT 1 FROM ticket_events e WHERE e.tenant_id=t.tenant_id AND e.ticket_id=t.id AND e.event_type IN ('created','department_changed') AND e.origin='observed' GROUP BY CASE WHEN e.event_type='created' THEN e.payload->>'department' ELSE e.payload->>'to' END HAVING COUNT(*)>1) GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 50`, "Повторное посещение отдела по записанным событиям; пропущенная история не восстановлена"},

		{"backlog", "Длительное ожидание обработки", "В подразделении накопились обращения старше семи календарных дней.", `SELECT COALESCE(department,'Unassigned'),'department',COUNT(*),(array_agg(id ORDER BY id DESC))[1:20],COALESCE(SUM(work_seconds),0) FROM filtered WHERE display_status<>'closed' AND created_at<NOW() AT TIME ZONE 'UTC'-INTERVAL '7 days' GROUP BY 1 HAVING COUNT(*)>=5 ORDER BY COUNT(*) DESC LIMIT 50`, "Открытые обращения старше семи дней; это сигнал проверки, а не доказанная нехватка сотрудников"},
	}
	for _, q := range queries {
		rows, e := tx.Query(ctx, analyticsBaseSQL+q.sql, args...)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var item Opportunity
			var key string
			var seconds float64
			if err = rows.Scan(&item.Department, &key, &item.Count, &item.TicketIDs, &seconds); err != nil {
				break
			}
			item.ID = opportunityID(security.TenantID(ctx), q.kind, item.Department, key)
			item.Kind = q.kind
			item.Title = q.title
			item.Description = q.description
			item.Confidence = q.confidence
			item.WorkingHours = seconds / 3600
			item.State = "new"
			item.Current = true
			item.Tasks = []OptimizationTask{}
			item.From = f.From
			item.To = f.To
			item.Currency = c.Economics.Currency
			item.Method = "Измеренные часы — сохранённое активное время. Потенциал — сценарная верхняя оценка, не достигнутый эффект."
			if q.kind == "exact_repeats" || q.kind == "knowledge_gap" {
				hours := float64(item.Count-1) * c.Economics.BaselineMinutes / 60
				rate := c.Economics.HourlyRate
				if configured, ok := c.Economics.DepartmentRates[item.Department]; ok {
					rate = configured
				}
				value := hours * rate
				item.PotentialHours = &hours
				item.PotentialValue = &value
			}
			result = append(result, item)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil || rowErr != nil {
			return nil, fmt.Errorf("read opportunities: %v %v", err, rowErr)
		}
	}
	semanticRows, e := tx.Query(ctx, analyticsBaseSQL+`SELECT payload FROM optimization_semantic_clusters s WHERE tenant_id=$1 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(s.payload->'ticket_ids') AS x(value) WHERE x.value::integer NOT IN (SELECT id FROM filtered)) ORDER BY updated_at DESC LIMIT 50`, args...)
	if e != nil {
		return nil, e
	}
	for semanticRows.Next() {
		var raw []byte
		if err = semanticRows.Scan(&raw); err != nil {
			break
		}
		var item Opportunity
		if err = json.Unmarshal(raw, &item); err != nil {
			break
		}
		item.Current = true
		result = append(result, item)
	}
	semanticErr := semanticRows.Err()
	semanticRows.Close()
	if err != nil || semanticErr != nil {
		return nil, fmt.Errorf("semantic groups unavailable")
	}
	rows, err := tx.Query(ctx, `SELECT id,state,assignee_id,note,revision,baseline,tasks FROM optimization_decisions WHERE tenant_id=$1 ORDER BY updated_at DESC LIMIT 300`, security.TenantID(ctx))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, state, assignee, note string
		var rev int64
		var raw, tasks []byte
		if err = rows.Scan(&id, &state, &assignee, &note, &rev, &raw, &tasks); err != nil {
			break
		}
		var baseline Opportunity
		if err = json.Unmarshal(raw, &baseline); err != nil {
			break
		}
		var taskList []OptimizationTask
		if err = json.Unmarshal(tasks, &taskList); err != nil {
			break
		}
		found := false
		for i := range result {
			if result[i].ID == id {
				result[i].State = state
				result[i].Assignee = assignee
				result[i].Note = note
				result[i].Revision = rev
				result[i].Tasks = taskList
				result[i].BaselineCount = baseline.Count
				found = true
				break
			}
		}
		if !found {
			baseline.State = state
			baseline.Assignee = assignee
			baseline.Note = note
			baseline.Revision = rev
			baseline.Tasks = taskList
			baseline.BaselineCount = baseline.Count
			baseline.Current = false
			result = append(result, baseline)
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		return nil, fmt.Errorf("read decisions: %v %v", err, rowErr)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
func (h *TicketsHandler) Opportunities(w http.ResponseWriter, r *http.Request) {
	p, ok := analyticsPrincipal(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	c, err := settings.LoadPlatform(ctx, h.db, security.TenantID(ctx))
	if err != nil {
		writeError(w, 503, "Не удалось загрузить настройки")
		return
	}
	items, err := h.discoverOpportunities(ctx, r.WithContext(ctx), c)
	if err != nil {
		h.logger.Error("optimization failed", "error", err.Error())
		writeError(w, 503, "Не удалось проанализировать обращения")
		return
	}
	if p.Role == "analyst" {
		for i := range items {
			items[i].TicketIDs = nil
		}
	}
	semantic := "not_run"
	for _, item := range items {
		if item.Kind == "semantic" {
			semantic = "stored_model_groups"
		}
	}
	writeJSON(w, 200, map[string]any{"items": items, "semantic_analysis": semantic, "method": "Детерминированные сигналы по реальным обращениям. Семантическая близость не заявляется без отдельного анализа моделью."})
}
func (h *TicketsHandler) DecideOpportunity(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || p.Role != "admin" {
		writeError(w, 403, "Изменять рекомендации может администратор")
		return
	}
	var input struct {
		State    string             `json:"state"`
		Assignee string             `json:"assignee_id"`
		Note     string             `json:"note"`
		Revision int64              `json:"revision"`
		Tasks    []OptimizationTask `json:"tasks"`
	}
	if !decodeDialogue(w, r, &input) {
		return
	}
	if !hasValue(input.State, "accepted", "rejected", "deferred", "implemented") || len([]rune(input.Note)) > 2000 || len(input.Tasks) > 30 {
		writeError(w, 400, "Некорректное решение")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tenant := security.TenantID(ctx)
	c, err := settings.LoadPlatform(ctx, h.db, tenant)
	if err != nil {
		writeError(w, 503, "Не удалось загрузить настройки")
		return
	}
	items, err := h.discoverOpportunities(ctx, r.WithContext(ctx), c)
	if err != nil {
		writeError(w, 503, "Не удалось проверить рекомендацию")
		return
	}
	var chosen *Opportunity
	for i := range items {
		if items[i].ID == r.PathValue("id") {
			chosen = &items[i]
			break
		}
	}
	if chosen == nil {
		writeError(w, 404, "Рекомендация не найдена")
		return
	}
	if chosen.Revision != input.Revision {
		writeError(w, 409, "Рекомендация уже изменена")
		return
	}
	assignees := []string{input.Assignee}
	taskIDs := map[string]bool{}
	for i := range input.Tasks {
		task := &input.Tasks[i]
		task.Title = strings.TrimSpace(task.Title)
		if task.ID == "" {
			task.ID = opportunityID(tenant, chosen.ID, fmt.Sprint(i), task.Title)
		}
		if len(task.ID) > 64 || taskIDs[task.ID] || task.Title == "" || len([]rune(task.Title)) > 200 || !hasValue(task.State, "todo", "doing", "done") {
			writeError(w, 400, "Некорректная задача")
			return
		}
		taskIDs[task.ID] = true
		assignees = append(assignees, task.Assignee)
	}
	for _, id := range assignees {
		if id == "" {
			continue
		}
		var found bool
		if err = h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE tenant_id=$1 AND id=$2 AND is_active)`, tenant, id).Scan(&found); err != nil || !found {
			writeError(w, 400, "Ответственный не найден в организации")
			return
		}
	}
	tx, err := h.beginTicketTx(ctx)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить решение")
		return
	}
	defer tx.Rollback(ctx)
	baseline, _ := json.Marshal(chosen)
	tasks, _ := json.Marshal(input.Tasks)
	if input.Tasks == nil {
		tasks = []byte("[]")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO optimization_decisions(tenant_id,id,state,assignee_id,note,baseline,tasks) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id,id) DO UPDATE SET state=EXCLUDED.state,assignee_id=EXCLUDED.assignee_id,note=EXCLUDED.note,tasks=EXCLUDED.tasks,revision=optimization_decisions.revision+1,updated_at=NOW() WHERE optimization_decisions.revision=$8`, tenant, chosen.ID, input.State, input.Assignee, input.Note, baseline, tasks, input.Revision)
	if err != nil {
		writeError(w, 503, "Не удалось сохранить решение")
		return
	}
	if tag.RowsAffected() != 1 {
		writeError(w, 409, "Рекомендация уже изменена")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'OPTIMIZATION_DECISION',$2,$3)`, tenant, p.ID, chosen.ID+":"+input.State); err != nil {
		writeError(w, 503, "Не удалось сохранить аудит")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 503, "Не удалось сохранить решение")
		return
	}
	writeJSON(w, 200, map[string]any{"saved": true, "revision": input.Revision + 1})
}
