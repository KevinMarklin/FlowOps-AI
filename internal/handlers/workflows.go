package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flowops-ai/internal/security"
	"flowops-ai/internal/workflows"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

func operationID() string {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(key[:])
}

func (h *TicketsHandler) WorkflowDefinitions(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || !hasValue(p.Role, "admin", "operator") {
		writeError(w, 403, "Нет доступа")
		return
	}
	tenant := p.TenantID
	if r.Method == "GET" {
		rows, err := h.db.Query(r.Context(), `SELECT definition,revision,active FROM workflow_definitions WHERE tenant_id=$1 ORDER BY name LIMIT 200`, tenant)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить процессы")
			return
		}
		defer rows.Close()
		items := []workflows.Definition{}
		for rows.Next() {
			var raw []byte
			var d workflows.Definition
			var revision int64
			var active bool
			if err = rows.Scan(&raw, &revision, &active); err != nil {
				break
			}
			if err = json.Unmarshal(raw, &d); err != nil {
				break
			}
			d.Revision = revision
			d.Active = active
			if p.Role != "admin" {
				d.Nodes = nil
			}
			items = append(items, d)
		}
		if err != nil || rows.Err() != nil {
			writeError(w, 503, "Не удалось прочитать процессы")
			return
		}
		writeJSON(w, 200, items)
		return
	}
	if p.Role != "admin" {
		writeError(w, 403, "Процессы настраивает администратор")
		return
	}
	var d workflows.Definition
	if !decodeDialogue(w, r, &d) {
		return
	}
	if err := d.Validate(h.callbacks.Validate); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if r.PathValue("id") != "" && r.PathValue("id") != d.ID {
		writeError(w, 400, "Код процесса не совпадает")
		return
	}
	formRaw, formErr := workflowForm(r.Context(), h.db, tenant, 0)
	if formErr != nil {
		writeError(w, 503, "Не удалось проверить форму")
		return
	}
	if err := validateWorkflowFields(d, formRaw, true); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	for _, n := range d.Nodes {
		if n.Assignee != "" {
			var exists bool
			if err := h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE tenant_id=$1 AND id=$2 AND is_active AND ($3<> 'assign' OR role IN ('operator','admin')))`, tenant, n.Assignee, n.Kind).Scan(&exists); err != nil || !exists {
				writeError(w, 400, "Ответственный недоступен")
				return
			}
		}
	}
	raw, _ := json.Marshal(d)
	tx, err := h.beginTicketTx(r.Context())
	if err != nil {
		writeError(w, 503, "Не удалось сохранить процесс")
		return
	}
	defer tx.Rollback(r.Context())
	if d.Revision == 0 {
		_, err = tx.Exec(r.Context(), `INSERT INTO workflow_definitions(tenant_id,id,name,active,definition) VALUES($1,$2,$3,$4,$5)`, tenant, d.ID, strings.TrimSpace(d.Name), d.Active, raw)
	} else {
		tag, updateErr := tx.Exec(r.Context(), `UPDATE workflow_definitions SET name=$3,active=$4,definition=$5,revision=revision+1 WHERE tenant_id=$1 AND id=$2 AND revision=$6`, tenant, d.ID, strings.TrimSpace(d.Name), d.Active, raw, d.Revision)
		err = updateErr
		if err == nil && tag.RowsAffected() != 1 {
			err = pgx.ErrNoRows
		}
	}
	if err != nil {
		writeError(w, 409, "Процесс уже существует или версия устарела")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(tenant_id,event_type,actor,details) VALUES($1,'WORKFLOW_DEFINED',$2,$3)`, tenant, p.ID, d.ID); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 503, "Не удалось сохранить процесс")
		return
	}
	d.Revision++
	writeJSON(w, 200, d)
}

func (h *TicketsHandler) TicketWorkflows(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(r)
	if !ok {
		writeError(w, 400, "Некорректный тикет")
		return
	}
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok {
		writeError(w, 401, "Необходим вход")
		return
	}
	if r.Method == "GET" {
		rows, err := h.db.Query(r.Context(), `SELECT r.id,r.definition_id,r.state,r.created_at,r.deadline,COALESCE((SELECT jsonb_agg(jsonb_build_object('id',s.node_id,'state',s.state,'attempts',s.attempts,'error',s.error) ORDER BY s.node_id) FROM workflow_steps s WHERE s.run_id=r.id),'[]') FROM workflow_runs r WHERE r.tenant_id=$1 AND r.ticket_id=$2 ORDER BY r.created_at DESC LIMIT 30`, p.TenantID, id)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить выполнение")
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var run, definition, state string
			var created, deadline any
			var raw []byte
			if err = rows.Scan(&run, &definition, &state, &created, &deadline, &raw); err != nil {
				break
			}
			items = append(items, map[string]any{"id": run, "definition_id": definition, "state": state, "created_at": created, "deadline": deadline, "steps": json.RawMessage(raw)})
		}
		if err != nil || rows.Err() != nil {
			writeError(w, 503, "Не удалось прочитать выполнение")
			return
		}
		writeJSON(w, 200, items)
		return
	}
	if !hasValue(p.Role, "admin", "operator") {
		writeError(w, 403, "Нет доступа к запуску")
		return
	}
	var input struct {
		DefinitionID string `json:"definition_id"`
		Key          string `json:"key"`
	}
	if !decodeDialogue(w, r, &input) {
		return
	}
	if !requestKeyPattern.MatchString(input.Key) {
		writeError(w, 400, "Нужен ключ повторной отправки")
		return
	}
	tx, err := h.beginTicketTx(r.Context())
	if err != nil {
		writeError(w, 503, "Не удалось запустить процесс")
		return
	}
	defer tx.Rollback(r.Context())
	var status string
	if err = tx.QueryRow(r.Context(), `SELECT status FROM tickets WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, p.TenantID, id).Scan(&status); err != nil {
		writeError(w, 404, "Тикет не найден")
		return
	}
	var previous, previousDefinition string
	err = tx.QueryRow(r.Context(), `SELECT id,definition_id FROM workflow_runs WHERE tenant_id=$1 AND ticket_id=$2 AND request_key=$3`, p.TenantID, id, input.Key).Scan(&previous, &previousDefinition)
	if err == nil {
		if previousDefinition != input.DefinitionID {
			writeError(w, 409, "Ключ уже использован для другого процесса")
			return
		}
		writeJSON(w, 200, map[string]any{"id": previous})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 503, "Не удалось проверить запуск")
		return
	}
	if dialogueClosed(status) {
		writeError(w, 409, "Тикет закрыт")
		return
	}
	var raw []byte
	if err = tx.QueryRow(r.Context(), `SELECT definition FROM workflow_definitions WHERE tenant_id=$1 AND id=$2 AND active FOR SHARE`, p.TenantID, input.DefinitionID).Scan(&raw); err != nil {
		writeError(w, 404, "Активный процесс не найден")
		return
	}
	var d workflows.Definition
	if json.Unmarshal(raw, &d) != nil || d.Validate(h.callbacks.Validate) != nil {
		writeError(w, 409, "Процесс недоступен")
		return
	}
	formRaw, formErr := workflowForm(r.Context(), tx, p.TenantID, id)
	if formErr != nil {
		writeError(w, 503, "Не удалось проверить форму тикета")
		return
	}
	if fieldErr := validateWorkflowFields(d, formRaw, false); fieldErr != nil {
		writeError(w, 400, fieldErr.Error())
		return
	}
	var count int
	if err = tx.QueryRow(r.Context(), `SELECT COUNT(*) FROM workflow_runs WHERE tenant_id=$1 AND ticket_id=$2 AND state='running'`, p.TenantID, id).Scan(&count); err != nil || count >= 10 {
		writeError(w, 409, "Слишком много активных процессов")
		return
	}
	run := operationID()
	if _, err = tx.Exec(r.Context(), `INSERT INTO workflow_runs(id,tenant_id,ticket_id,definition_id,definition,requester_id,request_key,deadline) VALUES($1,$2,$3,$4,$5,$6,$7,NOW()+$8::integer*INTERVAL '1 minute')`, run, p.TenantID, id, input.DefinitionID, raw, p.ID, input.Key, d.TimeoutMinutes); err != nil {
		writeError(w, 503, "Не удалось сохранить запуск")
		return
	}
	for _, n := range d.Nodes {
		if _, err = tx.Exec(r.Context(), `INSERT INTO workflow_steps(run_id,node_id) VALUES($1,$2)`, run, n.ID); err != nil {
			writeError(w, 503, "Не удалось сохранить шаги")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES($1,$2,'workflow_started',$3,jsonb_build_object('run_id',$4::text,'definition_id',$5::text))`, p.TenantID, id, p.ID, run, d.ID); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 503, "Не удалось сохранить запуск")
		return
	}
	writeJSON(w, 201, map[string]any{"id": run})
}

func (h *TicketsHandler) WorkflowTasks(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok {
		writeError(w, 401, "Необходим вход")
		return
	}
	tenant := p.TenantID
	if r.Method == "GET" {
		rows, err := h.db.Query(r.Context(), `SELECT a.id,a.run_id,a.node_id,a.ticket_id,a.kind,a.title,a.assignee_id,a.state,a.created_at,(flowops_ticket_visible(t.tenant_id,$2,t.department,t.requester_id,t.id) AND ($4<>'employee' OR t.requester_id=$2)) FROM workflow_tasks a JOIN tickets t ON t.id=a.ticket_id AND t.tenant_id=a.tenant_id WHERE a.tenant_id=$1 AND ($3 OR a.assignee_id=$2) AND (a.assignee_id=$2 OR flowops_ticket_visible(t.tenant_id,$2,t.department,t.requester_id,t.id)) ORDER BY a.id DESC LIMIT 100`, tenant, p.ID, p.Role == "admin", p.Role)
		if err != nil {
			writeError(w, 503, "Не удалось загрузить задачи")
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id int64
			var run, node, kind, title, assignee, state string
			var ticket int
			var created any
			var accessible bool
			if err = rows.Scan(&id, &run, &node, &ticket, &kind, &title, &assignee, &state, &created, &accessible); err != nil {
				break
			}
			items = append(items, map[string]any{"id": id, "run_id": run, "node_id": node, "ticket_id": ticket, "kind": kind, "title": title, "assignee_id": assignee, "state": state, "created_at": created, "ticket_access": accessible})
		}
		if err != nil || rows.Err() != nil {
			writeError(w, 503, "Не удалось прочитать задачи")
			return
		}
		writeJSON(w, 200, items)
		return
	}
	task, err := workflows.TaskID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "Некорректная задача")
		return
	}
	var input struct {
		Action string `json:"action"`
	}
	if !decodeDialogue(w, r, &input) {
		return
	}
	if !hasValue(input.Action, "complete", "approve", "reject") {
		writeError(w, 400, "Некорректное действие")
		return
	}
	tx, err := h.beginTicketTx(r.Context())
	if err != nil {
		writeError(w, 503, "Задача недоступна")
		return
	}
	defer tx.Rollback(r.Context())
	// Lock the run before its task, matching the worker lock order.
	var run string
	if err = tx.QueryRow(r.Context(), `SELECT a.run_id FROM workflow_tasks a JOIN tickets t ON t.id=a.ticket_id AND t.tenant_id=a.tenant_id WHERE a.tenant_id=$1 AND a.id=$2 AND ($4 OR a.assignee_id=$3) AND (a.assignee_id=$3 OR flowops_ticket_visible(t.tenant_id,$3,t.department,t.requester_id,t.id))`, tenant, task, p.ID, p.Role == "admin").Scan(&run); err != nil {
		writeError(w, 404, "Задача не найдена")
		return
	}
	var runState string
	if err = tx.QueryRow(r.Context(), `SELECT state FROM workflow_runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, run).Scan(&runState); err != nil {
		writeError(w, 409, "Процесс завершён")
		return
	}
	var state, kind string
	var ticket int
	if err = tx.QueryRow(r.Context(), `SELECT state,kind,ticket_id FROM workflow_tasks WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, task).Scan(&state, &kind, &ticket); err != nil {
		writeError(w, 404, "Задача не найдена")
		return
	}
	if runState != "running" && !(kind == "notify" && runState == "completed") {
		writeError(w, 409, "Процесс завершён")
		return
	}
	if state != "open" {
		writeError(w, 409, "Задача уже обработана")
		return
	}
	if kind == "approve" && input.Action == "complete" || kind != "approve" && input.Action != "complete" {
		writeError(w, 400, "Действие не соответствует задаче")
		return
	}
	next := "completed"
	if input.Action == "reject" {
		next = "rejected"
	}
	if _, err = tx.Exec(r.Context(), `UPDATE workflow_tasks SET state=$3,actor=$4,completed_at=NOW() WHERE tenant_id=$1 AND id=$2`, tenant, task, next, p.ID); err != nil {
		writeError(w, 503, "Не удалось сохранить решение")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES($1,$2,'workflow_task_decided',$3,jsonb_build_object('task_id',$4::bigint,'state',$5::text))`, tenant, ticket, p.ID, task, next); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 503, "Не удалось сохранить решение")
		return
	}
	writeJSON(w, 200, map[string]any{"updated": true})
}

func (h *TicketsHandler) CancelWorkflow(w http.ResponseWriter, r *http.Request) {
	p, ok := security.CurrentPrincipal(r.Context())
	if !ok || !hasValue(p.Role, "admin", "operator") {
		writeError(w, 403, "Нет доступа")
		return
	}
	tx, err := h.beginTicketTx(r.Context())
	if err != nil {
		writeError(w, 503, "Процесс недоступен")
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	var ticket int
	if err = tx.QueryRow(r.Context(), `SELECT r.id,r.ticket_id FROM workflow_runs r JOIN tickets t ON t.id=r.ticket_id AND t.tenant_id=r.tenant_id WHERE r.tenant_id=$1 AND r.id=$2 AND r.state='running' AND flowops_ticket_visible(t.tenant_id,$3,t.department,t.requester_id,t.id) FOR UPDATE OF r`, p.TenantID, r.PathValue("id"), p.ID).Scan(&id, &ticket); err != nil {
		writeError(w, 404, "Активный процесс не найден")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE workflow_runs SET state='cancelled',updated_at=NOW() WHERE id=$1`, id); err != nil {
		writeError(w, 503, "Не удалось отменить процесс")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE workflow_tasks SET state='cancelled',completed_at=NOW() WHERE run_id=$1 AND state='open'`, id); err != nil {
		writeError(w, 503, "Не удалось отменить задачи")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE workflow_steps SET state='cancelled',finished_at=NOW() WHERE run_id=$1 AND state IN ('pending','waiting')`, id); err != nil {
		writeError(w, 503, "Не удалось отменить шаги")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES($1,$2,'workflow_cancelled',$3,jsonb_build_object('run_id',$4::text))`, p.TenantID, ticket, p.ID, id); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, 503, "Не удалось сохранить отмену")
		return
	}
	writeJSON(w, 200, map[string]any{"cancelled": true})
}
