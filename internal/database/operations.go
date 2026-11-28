package database

const OperationsSchema = `
CREATE TABLE IF NOT EXISTS workflow_definitions(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id),id TEXT NOT NULL,name TEXT NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1,active BOOLEAN NOT NULL DEFAULT TRUE,definition JSONB NOT NULL,
 PRIMARY KEY(tenant_id,id)
);
CREATE TABLE IF NOT EXISTS workflow_runs(
 id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,ticket_id INTEGER NOT NULL REFERENCES tickets(id),
 definition_id TEXT NOT NULL,definition JSONB NOT NULL,state TEXT NOT NULL DEFAULT 'running',
 requester_id TEXT NOT NULL,request_key TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 deadline TIMESTAMPTZ NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(tenant_id,ticket_id,request_key),FOREIGN KEY(tenant_id,definition_id) REFERENCES workflow_definitions(tenant_id,id)
);
CREATE INDEX IF NOT EXISTS workflow_pending ON workflow_runs(updated_at) WHERE state='running';
CREATE TABLE IF NOT EXISTS workflow_steps(
 run_id TEXT NOT NULL REFERENCES workflow_runs(id),node_id TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',
 attempts INTEGER NOT NULL DEFAULT 0,next_attempt TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 started_at TIMESTAMPTZ,finished_at TIMESTAMPTZ,error TEXT NOT NULL DEFAULT '',PRIMARY KEY(run_id,node_id)
);
CREATE TABLE IF NOT EXISTS workflow_tasks(
 id BIGSERIAL PRIMARY KEY,tenant_id TEXT NOT NULL,run_id TEXT NOT NULL,node_id TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id),kind TEXT NOT NULL,title TEXT NOT NULL,
 assignee_id TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'open',actor TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),completed_at TIMESTAMPTZ,
 UNIQUE(run_id,node_id),FOREIGN KEY(run_id,node_id) REFERENCES workflow_steps(run_id,node_id)
);
CREATE INDEX IF NOT EXISTS workflow_tasks_inbox ON workflow_tasks(tenant_id,assignee_id,state);
CREATE TABLE IF NOT EXISTS organization_assets(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id),id TEXT NOT NULL,name TEXT NOT NULL,kind TEXT NOT NULL,
 unit_id TEXT NOT NULL DEFAULT '',description TEXT NOT NULL DEFAULT '',active BOOLEAN NOT NULL DEFAULT TRUE,
 revision BIGINT NOT NULL DEFAULT 1,PRIMARY KEY(tenant_id,id)
);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS asset_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS asset_snapshot JSONB NOT NULL DEFAULT '{}';
CREATE TABLE IF NOT EXISTS report_schedules(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id),id TEXT NOT NULL,owner_id TEXT NOT NULL,
 name TEXT NOT NULL,filters JSONB NOT NULL DEFAULT '{}',format TEXT NOT NULL,period_days INTEGER NOT NULL,
 interval_days INTEGER NOT NULL,active BOOLEAN NOT NULL DEFAULT TRUE,revision BIGINT NOT NULL DEFAULT 1,
 next_run TIMESTAMPTZ NOT NULL,PRIMARY KEY(tenant_id,id)
);
CREATE INDEX IF NOT EXISTS report_schedules_due ON report_schedules(next_run) WHERE active;
CREATE TABLE IF NOT EXISTS report_artifacts(
 id BIGSERIAL PRIMARY KEY,tenant_id TEXT NOT NULL,schedule_id TEXT NOT NULL,owner_id TEXT NOT NULL,
 period_end TIMESTAMPTZ NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),format TEXT NOT NULL,
 content BYTEA NOT NULL,UNIQUE(tenant_id,schedule_id,period_end),
 FOREIGN KEY(tenant_id,schedule_id) REFERENCES report_schedules(tenant_id,id)
);
CREATE OR REPLACE FUNCTION flowops_capture_assignment_event() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.assignee_id IS DISTINCT FROM OLD.assignee_id THEN
  INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES(NEW.tenant_id,NEW.id,'assignee_changed',COALESCE(NULLIF(current_setting('flowops.actor',TRUE),''),'system'),jsonb_build_object('from',OLD.assignee_id,'to',NEW.assignee_id));
 END IF;
 IF NEW.sla_paused_at IS DISTINCT FROM OLD.sla_paused_at THEN
  INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES(NEW.tenant_id,NEW.id,CASE WHEN NEW.sla_paused_at IS NULL THEN 'sla_resumed' ELSE 'sla_paused' END,COALESCE(NULLIF(current_setting('flowops.actor',TRUE),''),'system'),'{}');
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ticket_assignment_events ON tickets;
CREATE TRIGGER ticket_assignment_events AFTER UPDATE OF assignee_id,status ON tickets FOR EACH ROW EXECUTE FUNCTION flowops_capture_assignment_event();

ALTER TABLE report_schedules ADD COLUMN IF NOT EXISTS manual_requested BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE report_schedules ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS knowledge_checks(id BIGSERIAL PRIMARY KEY,tenant_id TEXT NOT NULL,actor TEXT NOT NULL,input_revisions JSONB NOT NULL,result JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE INDEX IF NOT EXISTS knowledge_checks_tenant ON knowledge_checks(tenant_id,id DESC);

CREATE OR REPLACE FUNCTION flowops_capture_employee_message() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.role='employee' AND NEW.source_key NOT IN ('initial','legacy-reply','initial-answer') THEN
  INSERT INTO ticket_events(tenant_id,ticket_id,event_type,occurred_at,actor,payload) VALUES(NEW.tenant_id,NEW.ticket_id,'employee_message',NEW.created_at,COALESCE(NULLIF(NEW.actor,''),'employee'),jsonb_build_object('message_id',NEW.id));
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ticket_employee_message_event ON ticket_messages;
CREATE TRIGGER ticket_employee_message_event AFTER INSERT ON ticket_messages FOR EACH ROW EXECUTE FUNCTION flowops_capture_employee_message();

CREATE OR REPLACE FUNCTION flowops_capture_initial_classification() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES(NEW.tenant_id,NEW.id,'initial_classification','system',jsonb_build_object('department',NEW.department,'priority',NEW.priority));
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ticket_initial_classification ON tickets;
CREATE TRIGGER ticket_initial_classification AFTER INSERT ON tickets FOR EACH ROW EXECUTE FUNCTION flowops_capture_initial_classification();
`
