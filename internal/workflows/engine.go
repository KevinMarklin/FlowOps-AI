package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flowops-ai/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strconv"
	"time"
)

type Engine struct {
	DB        *pgxpool.Pool
	Callbacks *security.Callbacks
}

// Tick claims runs with row locks. Local effects and progress commit together;
// remote retries carry the same key and require an idempotent receiver.
func (e *Engine) Tick(ctx context.Context) error {
	for i := 0; i < 20; i++ {
		worked, err := e.advance(ctx)
		if err != nil {
			return err
		}
		if !worked {
			return nil
		}
	}
	return nil
}
func (e *Engine) advance(ctx context.Context) (bool, error) {
	tx, err := e.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id, tenant, actor string
	var ticket int
	var raw []byte
	var deadline, created time.Time
	err = tx.QueryRow(ctx, `SELECT id,tenant_id,ticket_id,requester_id,definition,deadline,created_at FROM workflow_runs WHERE state='running' AND updated_at<=NOW()-INTERVAL '1 second' ORDER BY updated_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &tenant, &ticket, &actor, &raw, &deadline, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('flowops.actor',$1,TRUE)`, actor); err != nil {
		return false, err
	}
	var d Definition
	if json.Unmarshal(raw, &d) != nil {
		return false, errors.New("invalid persisted workflow")
	}
	var ticketRaw []byte
	var closed, authorized bool
	err = tx.QueryRow(ctx, `SELECT to_jsonb(t),status IN ('closed','auto_resolved'),flowops_ticket_visible(tenant_id,$3,department,requester_id,id) FROM tickets t WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, ticket, actor).Scan(&ticketRaw, &closed, &authorized)
	if err != nil {
		return false, err
	}
	if actor != "owner" {
		var active bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members m WHERE m.tenant_id=$1 AND m.id=$2 AND m.is_active AND m.role IN ('admin','operator') AND NOT EXISTS(SELECT 1 FROM organization_member_roles a JOIN organization_access_roles r ON r.tenant_id=a.tenant_id AND r.id=a.role_id WHERE a.tenant_id=m.tenant_id AND a.member_id=m.id AND r.permissions IS NOT NULL AND r.permissions<>'null' AND NOT r.permissions @> '["automation_manage","ticket_edit","ticket_assign"]'))`, tenant, actor).Scan(&active)
		if err != nil {
			return false, err
		}
		authorized = authorized && active
	}
	if closed || !authorized || time.Now().After(deadline) {
		state := "cancelled"
		if !closed && authorized {
			state = "timed_out"
		}
		return true, finish(ctx, tx, id, tenant, ticket, state)
	}
	var data map[string]any
	if json.Unmarshal(ticketRaw, &data) != nil {
		return false, errors.New("invalid ticket data")
	}
	rows, err := tx.Query(ctx, `SELECT node_id,state,started_at IS NOT NULL FROM workflow_steps WHERE run_id=$1`, id)
	if err != nil {
		return false, err
	}
	states := map[string]string{}
	started := map[string]bool{}
	for rows.Next() {
		var key, state string
		var began bool
		if err = rows.Scan(&key, &state, &began); err != nil {
			rows.Close()
			return false, err
		}
		states[key] = state
		started[key] = began
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, state := range states {
		if state == "failed" {
			return true, finish(ctx, tx, id, tenant, ticket, "failed")
		}
	}
	changed := false
	for _, n := range d.Nodes {
		state := states[n.ID]
		if state == "done" || state == "skipped" {
			continue
		}
		if state == "failed" {
			return true, finish(ctx, tx, id, tenant, ticket, "failed")
		}
		ready := true
		for _, parent := range n.After {
			if states[parent] != "done" && states[parent] != "skipped" {
				ready = false
			}
		}
		if !ready {
			continue
		}
		matches := true
		if !started[n.ID] && len(n.After) > 0 && n.Kind != "end" {
			allSkipped := true
			for _, parent := range n.After {
				if states[parent] != "skipped" {
					allSkipped = false
				}
			}
			if allSkipped {
				matches = false
			}
		}
		if !started[n.ID] && matches && len(n.If) > 0 {
			rule, _ := json.Marshal(map[string]any{"match": n.If})
			currentData, _ := json.Marshal(data)
			if err = tx.QueryRow(ctx, `SELECT flowops_rule_matches($1::jsonb,$2::jsonb,'{}'::jsonb)`, rule, currentData).Scan(&matches); err != nil {
				return false, err
			}
		}
		if !matches {
			if _, err = tx.Exec(ctx, `UPDATE workflow_steps SET state='skipped',finished_at=NOW() WHERE run_id=$1 AND node_id=$2`, id, n.ID); err != nil {
				return false, err
			}
			states[n.ID] = "skipped"
			changed = true
			continue
		}
		var attempts int
		var next time.Time
		if err = tx.QueryRow(ctx, `SELECT attempts,next_attempt FROM workflow_steps WHERE run_id=$1 AND node_id=$2`, id, n.ID).Scan(&attempts, &next); err != nil {
			return false, err
		}
		if next.After(time.Now()) {
			continue
		}
		complete := true
		var actionErr error
		actionTx, beginErr := tx.Begin(ctx)
		if beginErr != nil {
			return false, beginErr
		}
		switch n.Kind {
		case "start", "condition":
		case "end":
			if _, err = actionTx.Exec(ctx, `UPDATE workflow_steps SET state='done',started_at=COALESCE(started_at,NOW()),finished_at=NOW() WHERE run_id=$1 AND node_id=$2`, id, n.ID); err != nil {
				return false, err
			}
			if err = actionTx.Commit(ctx); err != nil {
				return false, err
			}
			return true, finish(ctx, tx, id, tenant, ticket, "completed")
		case "collect", "check":
			fields, _ := data["custom_data"].(map[string]any)
			for _, key := range n.Required {
				v, ok := fields[key]
				if !ok || emptyValue(v) {
					complete = false
				}
			}
		case "assign":
			var eligible bool
			actionErr = actionTx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members m JOIN tickets t ON t.tenant_id=m.tenant_id WHERE m.tenant_id=$1 AND m.id=$2 AND t.id=$3 AND m.is_active AND m.role IN ('operator','admin') AND flowops_ticket_visible(m.tenant_id,m.id,t.department,t.requester_id,t.id))`, tenant, n.Assignee, ticket).Scan(&eligible)
			if actionErr == nil && !eligible {
				actionErr = errors.New("assignee unavailable")
			}
			if actionErr == nil {
				_, actionErr = actionTx.Exec(ctx, `UPDATE tickets SET assignee_id=$3 WHERE tenant_id=$1 AND id=$2`, tenant, ticket, n.Assignee)
				if actionErr == nil {
					data["assignee_id"] = n.Assignee
				}
			}
		case "transfer":
			var eligible bool
			actionErr = actionTx.QueryRow(ctx, `SELECT $2 IN ('General','Unassigned') OR EXISTS(SELECT 1 FROM app_settings a CROSS JOIN jsonb_array_elements(COALESCE(a.builder_config->'departments','[]')) d WHERE a.tenant_id=$1 AND d->>'name'=$2)`, tenant, n.Department).Scan(&eligible)
			if actionErr == nil && !eligible {
				actionErr = errors.New("department unavailable")
			}
			if actionErr == nil {
				_, actionErr = actionTx.Exec(ctx, `UPDATE tickets SET department=$3 WHERE tenant_id=$1 AND id=$2`, tenant, ticket, n.Department)
				if actionErr == nil {
					data["department"] = n.Department
				}
			}
		case "task", "approve", "notify":
			_, actionErr = actionTx.Exec(ctx, `INSERT INTO workflow_tasks(tenant_id,run_id,node_id,ticket_id,kind,title,assignee_id) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(run_id,node_id) DO NOTHING`, tenant, id, n.ID, ticket, n.Kind, n.Title, n.Assignee)
			if actionErr == nil && n.Kind != "notify" {
				var taskState string
				actionErr = actionTx.QueryRow(ctx, `SELECT state FROM workflow_tasks WHERE run_id=$1 AND node_id=$2`, id, n.ID).Scan(&taskState)
				complete = taskState == "completed"
				if taskState == "rejected" {
					actionErr = errors.New("approval rejected")
					attempts = n.Retries
				}
			}
		case "wait":
			actionErr = actionTx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_events WHERE tenant_id=$1 AND ticket_id=$2 AND event_type=$3 AND origin='observed' AND occurred_at>=COALESCE((SELECT started_at FROM workflow_steps WHERE run_id=$4 AND node_id=$5),NOW()))`, tenant, ticket, n.Event, id, n.ID).Scan(&complete)
		case "http":
			if changed {
				_ = actionTx.Rollback(ctx)
				if _, err = tx.Exec(ctx, `UPDATE workflow_runs SET updated_at=NOW() WHERE id=$1`, id); err != nil {
					return false, err
				}
				return true, tx.Commit(ctx)
			}
			payload, _ := json.Marshal(map[string]any{"workflow_id": id, "ticket_id": ticket, "step_id": n.ID})
			actionErr = e.Callbacks.PostWithKey(ctx, n.Endpoint, bytes.NewReader(payload), "workflow:"+id+":"+n.ID)
		}
		if actionErr != nil {
			_ = actionTx.Rollback(ctx)
		} else if err = actionTx.Commit(ctx); err != nil {
			return false, err
		}
		nextState := "done"
		if !complete {
			nextState = "waiting"
		}
		safeError := ""
		if actionErr != nil {
			attempts++
			safeError = "Действие не выполнено"
			nextState = "pending"
			if attempts > n.Retries {
				nextState = "failed"
			}
		}
		delay := time.Duration(1<<min(attempts, 6)) * time.Second
		if _, err = tx.Exec(ctx, `UPDATE workflow_steps SET state=$3,attempts=$4,error=$5,started_at=COALESCE(started_at,NOW()),finished_at=CASE WHEN $3 IN ('done','failed') THEN NOW() ELSE NULL END,next_attempt=NOW()+$6::double precision*INTERVAL '1 second' WHERE run_id=$1 AND node_id=$2`, id, n.ID, nextState, attempts, safeError, delay.Seconds()); err != nil {
			return false, err
		}
		if states[n.ID] != nextState {
			changed = true
			if _, err = tx.Exec(ctx, `INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES($1,$2,'workflow_step',$3,jsonb_build_object('run_id',$4::text,'node_id',$5::text,'state',$6::text))`, tenant, ticket, actor, id, n.ID, nextState); err != nil {
				return false, err
			}
		}
		states[n.ID] = nextState
		if nextState == "failed" {
			return true, finish(ctx, tx, id, tenant, ticket, "failed")
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE workflow_runs SET updated_at=NOW() WHERE id=$1`, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
func finish(ctx context.Context, tx pgx.Tx, id, tenant string, ticket int, state string) error {
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state=$2,updated_at=NOW() WHERE id=$1`, id, state); err != nil {
		return err
	}
	if state == "completed" {
		if _, err := tx.Exec(ctx, `UPDATE workflow_tasks SET state='cancelled',completed_at=NOW() WHERE run_id=$1 AND state='open' AND kind<>'notify'`, id); err != nil {
			return err
		}
	}
	if state != "completed" {
		if _, err := tx.Exec(ctx, `UPDATE workflow_tasks SET state='cancelled',completed_at=NOW() WHERE run_id=$1 AND state='open'`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_steps SET state='cancelled',finished_at=NOW() WHERE run_id=$1 AND state IN ('pending','waiting')`, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_events(tenant_id,ticket_id,event_type,payload) VALUES($1,$2,'workflow_'||$3,jsonb_build_object('run_id',$4::text))`, tenant, ticket, state, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func TaskID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if id <= 0 {
		return 0, errors.New("invalid task")
	}
	return id, err
}

func emptyValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case []string:
		return len(v) == 0
	}
	return false
}
