package handlers

import (
	"context"
	"encoding/json"
	"flowops-ai/internal/security"
	"flowops-ai/internal/workflows"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestWorkflowParallelTasksIdempotencyAndApproval(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('wf-org','Workflow','x','wf-org');INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('wf-admin','wf-org','Admin','wf@example.org','admin','wf-admin')`)
	if err != nil {
		t.Fatal(err)
	}
	var ticket int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status,requester_id) VALUES('wf-org','Synthetic workflow','new','wf-admin') RETURNING id`).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	callbacks, _ := security.NewCallbacks("")
	h := NewTicketsHandler(db, nil, nil).WithCallbacks(callbacks)
	principal := security.Principal{ID: "wf-admin", TenantID: "wf-org", Role: "admin"}
	d := workflows.Definition{ID: "parallel", Name: "Parallel approval", Active: true, TimeoutMinutes: 60, Nodes: []workflows.Node{{ID: "start", Kind: "start"}, {ID: "task", Kind: "task", Title: "Prepare", If: map[string]string{"status": "new"}, After: []string{"start"}, Assignee: "wf-admin"}, {ID: "approval", Kind: "approve", Title: "Approve", After: []string{"start"}, Assignee: "wf-admin"}, {ID: "end", Kind: "end", After: []string{"task", "approval"}}}}
	raw, _ := json.Marshal(d)
	request := httptest.NewRequest("POST", "/api/v1/workflows", strings.NewReader(string(raw))).WithContext(security.WithPrincipal(ctx, principal))
	out := httptest.NewRecorder()
	h.WorkflowDefinitions(out, request)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var run string
	for i := 0; i < 2; i++ {
		request = httptest.NewRequest("POST", "/api/tickets/1/workflows", strings.NewReader(`{"definition_id":"parallel","key":"same-launch"}`)).WithContext(security.WithPrincipal(ctx, principal))
		request.SetPathValue("id", strconv.Itoa(ticket))
		out = httptest.NewRecorder()
		h.TicketWorkflows(out, request)
		if out.Code != 201 && out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
		var result struct {
			ID string `json:"id"`
		}
		json.Unmarshal(out.Body.Bytes(), &result)
		if run != "" && run != result.ID {
			t.Fatal("duplicate launch")
		}
		run = result.ID
	}
	tick := func() {
		t.Helper()
		if _, err = db.Exec(ctx, `UPDATE workflow_runs SET updated_at=NOW()-INTERVAL '2 seconds' WHERE id=$1`, run); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, `UPDATE workflow_steps SET next_attempt=NOW()-INTERVAL '1 second' WHERE run_id=$1`, run); err != nil {
			t.Fatal(err)
		}
		if err = (&workflows.Engine{DB: db, Callbacks: callbacks}).Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	var count int
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM workflow_tasks WHERE run_id=$1 AND state='open'`, run).Scan(&count); err != nil || count != 2 {
		t.Fatal("parallel tasks missing", count, err)
	}
	if _, err = db.Exec(ctx, `UPDATE tickets SET status='assigned_to_operator' WHERE id=$1`, ticket); err != nil {
		t.Fatal(err)
	}

	tick()
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM workflow_tasks WHERE run_id=$1`, run).Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate tasks", count, err)
	}
	var pendingState string
	if err = db.QueryRow(ctx, `SELECT state FROM workflow_steps WHERE run_id=$1 AND node_id='task'`, run).Scan(&pendingState); err != nil || pendingState != "waiting" {
		t.Fatal("started task condition was reevaluated", pendingState, err)
	}
	rows, err := db.Query(ctx, `SELECT id,kind FROM workflow_tasks WHERE run_id=$1`, run)
	if err != nil {
		t.Fatal(err)
	}
	tasks := map[int64]string{}
	for rows.Next() {
		var id int64
		var kind string
		rows.Scan(&id, &kind)
		tasks[id] = kind
	}
	rows.Close()
	for id, kind := range tasks {
		action := "complete"
		if kind == "approve" {
			action = "approve"
		}
		request = httptest.NewRequest("POST", "/api/v1/workflow-tasks/1", strings.NewReader(`{"action":"`+action+`"}`)).WithContext(security.WithPrincipal(ctx, principal))
		request.SetPathValue("id", strconv.Itoa(int(id)))
		out = httptest.NewRecorder()
		h.WorkflowTasks(out, request)
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	tick()
	var state string
	if err = db.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id=$1`, run).Scan(&state); err != nil || state != "completed" {
		t.Fatal("workflow not completed", state, err)
	}
}

func TestAssetTenantBindingDeactivationAndPNG(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('asset-a','A','x','asset-a'),('asset-b','B','x','asset-b')`)
	if err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, nil, nil)
	call := func(tenant, method, id, suffix, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/assets/"+id+suffix, strings.NewReader(body)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "owner", TenantID: tenant, Role: "admin"}))
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		h.Assets(w, r)
		return w
	}
	w := call("asset-a", "POST", "", "", `{"name":"Synthetic printer","kind":"printer","description":"Room 101"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var a OrganizationAsset
	json.Unmarshal(w.Body.Bytes(), &a)
	if strings.Contains(a.URL, "Synthetic") || strings.Contains(a.URL, "token") {
		t.Fatal("QR encodes private context", a.URL)
	}
	if w = call("asset-b", "GET", a.ID, "/context", ""); w.Code != 404 {
		t.Fatal("cross-tenant asset visible", w.Code)
	}
	if w = call("asset-a", "GET", a.ID, "/qr", ""); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || len(w.Body.Bytes()) < 100 {
		t.Fatal("QR PNG missing", w.Code)
	}
	a.Active = false
	raw, _ := json.Marshal(a)
	if w = call("asset-a", "PUT", a.ID, "", string(raw)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("asset-a", "GET", a.ID, "/context", ""); w.Code != 404 {
		t.Fatal("inactive asset visible")
	}
}

func TestScheduledReportDurableOutputAndRevocation(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('report-org','Reports','x','report-org');INSERT INTO tickets(tenant_id,raw_text,status) VALUES('report-org','Synthetic report','new');INSERT INTO report_schedules(tenant_id,id,owner_id,name,format,period_days,interval_days,next_run) VALUES('report-org','daily','owner','Daily','csv',30,1,NOW())`)
	if err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, nil, nil)
	for i := 0; i < 2; i++ {
		if err = h.GenerateScheduledReports(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var content []byte
	if err = db.QueryRow(ctx, `SELECT COUNT(*) FROM report_artifacts WHERE tenant_id='report-org'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate report", count, err)
	}
	if err = db.QueryRow(ctx, `SELECT content FROM report_artifacts WHERE tenant_id='report-org'`).Scan(&content); err != nil || len(content) < 100 {
		t.Fatal("missing report", err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO report_schedules(tenant_id,id,owner_id,name,format,period_days,interval_days,next_run) VALUES('report-org','revoked','missing-member','Revoked','csv',1,1,NOW())`); err != nil {
		t.Fatal(err)
	}
	if err = h.GenerateScheduledReports(ctx); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err = db.QueryRow(ctx, `SELECT active FROM report_schedules WHERE tenant_id='report-org' AND id='revoked'`).Scan(&active); err != nil || active {
		t.Fatal("revoked owner kept schedule", err)
	}
}

func TestWorkflowConditionsCompletenessAndObservedMessage(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('wf-wait','W','x','wf-wait');INSERT INTO app_settings(tenant_id,builder_config) VALUES('wf-wait','{"departments":[{"name":"IT"}]}')`)
	if err != nil {
		t.Fatal(err)
	}
	var ticket int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status,department,custom_data) VALUES('wf-wait','Synthetic wait','new','General','{"tags":[]}') RETURNING id`).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	d := workflows.Definition{ID: "wait", Name: "Wait", Active: true, TimeoutMinutes: 60, Nodes: []workflows.Node{{ID: "start", Kind: "start"}, {ID: "transfer", Kind: "transfer", After: []string{"start"}, Department: "IT"}, {ID: "check", Kind: "check", After: []string{"transfer"}, Required: []string{"tags"}, If: map[string]string{"department": "IT"}}, {ID: "wait", Kind: "wait", After: []string{"check"}, Event: "employee_message"}, {ID: "notify", Kind: "notify", After: []string{"wait"}, Title: "Synthetic notification"}, {ID: "end", Kind: "end", After: []string{"notify"}}}}
	raw, _ := json.Marshal(d)
	if _, err = db.Exec(ctx, `INSERT INTO workflow_definitions(tenant_id,id,name,definition) VALUES('wf-wait','wait','Wait',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO workflow_runs(id,tenant_id,ticket_id,definition_id,definition,requester_id,request_key,deadline) VALUES('wf-wait-run','wf-wait',$1,'wait',$2,'owner','once',NOW()+INTERVAL '1 hour')`, ticket, raw); err != nil {
		t.Fatal(err)
	}
	for _, n := range d.Nodes {
		if _, err = db.Exec(ctx, `INSERT INTO workflow_steps(run_id,node_id) VALUES('wf-wait-run',$1)`, n.ID); err != nil {
			t.Fatal(err)
		}
	}
	callbacks, _ := security.NewCallbacks("")
	engine := workflows.Engine{DB: db, Callbacks: callbacks}
	tick := func() {
		t.Helper()
		if _, err = db.Exec(ctx, `UPDATE workflow_runs SET updated_at=NOW()-INTERVAL '2 seconds' WHERE id='wf-wait-run';UPDATE workflow_steps SET next_attempt=NOW()-INTERVAL '1 second' WHERE run_id='wf-wait-run'`); err != nil {
			t.Fatal(err)
		}
		if err = engine.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	var state string
	if err = db.QueryRow(ctx, `SELECT state FROM workflow_steps WHERE run_id='wf-wait-run' AND node_id='check'`).Scan(&state); err != nil || state != "waiting" {
		t.Fatal("empty data or fresh department ignored", state, err)
	}
	if _, err = db.Exec(ctx, `UPDATE tickets SET custom_data='{"tags":["a"]}' WHERE id=$1`, ticket); err != nil {
		t.Fatal(err)
	}
	tick()
	if _, err = db.Exec(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,role,body,source_key) VALUES('wf-wait',$1,'employee','Synthetic initial','initial')`, ticket); err != nil {
		t.Fatal(err)
	}
	tick()
	if err = db.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id='wf-wait-run'`).Scan(&state); err != nil || state != "running" {
		t.Fatal("initial reconstruction satisfied wait", state, err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,role,body,source_key) VALUES('wf-wait',$1,'employee','Synthetic followup','real-followup')`, ticket); err != nil {
		t.Fatal(err)
	}
	tick()
	if err = db.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id='wf-wait-run'`).Scan(&state); err != nil || state != "completed" {
		t.Fatal("observed message did not resume workflow", state, err)
	}
	if err = db.QueryRow(ctx, `SELECT state FROM workflow_steps WHERE run_id='wf-wait-run' AND node_id='end'`).Scan(&state); err != nil || state != "done" {
		t.Fatal("end node left pending", state, err)
	}
	var task int64
	if err = db.QueryRow(ctx, `SELECT id FROM workflow_tasks WHERE run_id='wf-wait-run' AND kind='notify' AND state='open'`).Scan(&task); err != nil {
		t.Fatal("completion cancelled notification", err)
	}
	h := NewTicketsHandler(db, nil, nil)
	r := httptest.NewRequest("POST", "/api/v1/workflow-tasks/1", strings.NewReader(`{"action":"complete"}`)).WithContext(security.WithPrincipal(ctx, security.Principal{ID: "owner", TenantID: "wf-wait", Role: "admin"}))
	r.SetPathValue("id", strconv.FormatInt(task, 10))
	w := httptest.NewRecorder()
	h.WorkflowTasks(w, r)
	if w.Code != 200 {
		t.Fatal("cannot acknowledge completed workflow notification", w.Code, w.Body.String())
	}
}

func TestAssignedEmployeeApprovalDoesNotExposeTicket(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('approval-org','A','x','approval-org');INSERT INTO workspace_members(id,tenant_id,display_name,email,role,token_hash) VALUES('approver','approval-org','Approver','approve@example.org','employee','approval-employee')`)
	if err != nil {
		t.Fatal(err)
	}
	var ticket int
	if err = db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status,requester_id) VALUES('approval-org','Private unrelated ticket text','new','another-person') RETURNING id`).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"id":"approval","name":"Approval","timeout_minutes":60,"nodes":[{"id":"start","kind":"start"},{"id":"approve","kind":"approve","after":["start"],"assignee":"approver","title":"Confirm assigned task"},{"id":"end","kind":"end","after":["approve"]}]}`)
	if _, err = db.Exec(ctx, `INSERT INTO workflow_definitions(tenant_id,id,name,definition) VALUES('approval-org','approval','Approval',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO workflow_runs(id,tenant_id,ticket_id,definition_id,definition,requester_id,request_key,deadline,updated_at) VALUES('approval-run','approval-org',$1,'approval',$2,'owner','once',NOW()+INTERVAL '1 hour',NOW()-INTERVAL '2 seconds')`, ticket, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO workflow_steps(run_id,node_id) VALUES('approval-run','start'),('approval-run','approve'),('approval-run','end')`); err != nil {
		t.Fatal(err)
	}
	callbacks, _ := security.NewCallbacks("")
	if err = (&workflows.Engine{DB: db, Callbacks: callbacks}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	h := NewTicketsHandler(db, nil, nil)
	p := security.Principal{ID: "approver", TenantID: "approval-org", Role: "employee"}
	r := httptest.NewRequest("GET", "/api/v1/workflow-tasks", nil).WithContext(security.WithPrincipal(ctx, p))
	w := httptest.NewRecorder()
	h.WorkflowTasks(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "Private unrelated") || !strings.Contains(w.Body.String(), `"ticket_access":false`) {
		t.Fatal("assigned-task view incorrect", w.Code, w.Body.String())
	}
	var task int64
	if err = db.QueryRow(ctx, `SELECT id FROM workflow_tasks WHERE run_id='approval-run'`).Scan(&task); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("POST", "/api/v1/workflow-tasks/1", strings.NewReader(`{"action":"approve"}`)).WithContext(security.WithPrincipal(ctx, p))
	r.SetPathValue("id", strconv.FormatInt(task, 10))
	w = httptest.NewRecorder()
	h.WorkflowTasks(w, r)
	if w.Code != 200 {
		t.Fatal("assigned employee cannot approve", w.Code, w.Body.String())
	}
}

func TestWorkflowTerminalFailureStopsOtherActions(t *testing.T) {
	db := replyTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO workspaces(tenant_id,name,token_prefix,token_hash) VALUES('wf-fail','F','x','wf-fail')`); err != nil {
		t.Fatal(err)
	}
	var ticket int
	if err := db.QueryRow(ctx, `INSERT INTO tickets(tenant_id,raw_text,status) VALUES('wf-fail','Synthetic failure','new') RETURNING id`).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"id":"failure","name":"Failure","timeout_minutes":60,"nodes":[{"id":"start","kind":"start"},{"id":"bad","kind":"assign","after":["start"],"assignee":"missing-person"},{"id":"notify","kind":"notify","after":["start"]},{"id":"end","kind":"end","after":["bad","notify"]}]}`)
	if _, err := db.Exec(ctx, `INSERT INTO workflow_definitions(tenant_id,id,name,definition) VALUES('wf-fail','failure','Failure',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	for _, failedBefore := range []bool{false, true} {
		run := operationID()
		if _, err := db.Exec(ctx, `INSERT INTO workflow_runs(id,tenant_id,ticket_id,definition_id,definition,requester_id,request_key,deadline,updated_at) VALUES($1,'wf-fail',$2,'failure',$3,'owner',$1,NOW()+INTERVAL '1 hour',NOW()-INTERVAL '2 seconds')`, run, ticket, raw); err != nil {
			t.Fatal(err)
		}
		for _, node := range []string{"start", "bad", "notify", "end"} {
			state := "pending"
			if node == "start" {
				state = "done"
			}
			if failedBefore && node == "bad" {
				state = "failed"
			}
			if _, err := db.Exec(ctx, `INSERT INTO workflow_steps(run_id,node_id,state) VALUES($1,$2,$3)`, run, node, state); err != nil {
				t.Fatal(err)
			}
		}
		callbacks, _ := security.NewCallbacks("")
		if err := (&workflows.Engine{DB: db, Callbacks: callbacks}).Tick(ctx); err != nil {
			t.Fatal(err)
		}
		var state string
		var tasks int
		if err := db.QueryRow(ctx, `SELECT state,(SELECT COUNT(*) FROM workflow_tasks WHERE run_id=$1) FROM workflow_runs WHERE id=$1`, run).Scan(&state, &tasks); err != nil || state != "failed" || tasks != 0 {
			t.Fatal("known failure allowed later actions", failedBefore, state, tasks, err)
		}
	}
}
