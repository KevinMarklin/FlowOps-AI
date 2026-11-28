package database

const ProcessSchema = `
CREATE TABLE IF NOT EXISTS ticket_events (
 id BIGSERIAL PRIMARY KEY,
 tenant_id TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 event_type TEXT NOT NULL,
 occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 actor TEXT NOT NULL DEFAULT 'system',
 payload JSONB NOT NULL DEFAULT '{}',
 origin TEXT NOT NULL DEFAULT 'observed'
);
CREATE INDEX IF NOT EXISTS ticket_events_tenant_time ON ticket_events(tenant_id,occurred_at,id);
CREATE INDEX IF NOT EXISTS ticket_events_ticket ON ticket_events(tenant_id,ticket_id,event_type,occurred_at);
CREATE UNIQUE INDEX IF NOT EXISTS ticket_events_creation_once ON ticket_events(tenant_id,ticket_id) WHERE event_type='created';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS sla_policy_snapshot JSONB NOT NULL DEFAULT '{}';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS first_response_due_at TIMESTAMPTZ;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS resolution_due_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS tickets_resolution_due ON tickets(tenant_id,resolution_due_at) WHERE status NOT IN ('closed','auto_resolved');

CREATE OR REPLACE FUNCTION flowops_business_deadline(start_at TIMESTAMPTZ, minutes INTEGER, cfg JSONB) RETURNS TIMESTAMPTZ LANGUAGE plpgsql AS $$
DECLARE zone TEXT:=COALESCE(cfg->>'timezone','Europe/Moscow'); local_day DATE; a TIMESTAMPTZ; b TIMESTAMPTZ; cursor_at TIMESTAMPTZ:=start_at; remaining DOUBLE PRECISION:=minutes*60.0; available DOUBLE PRECISION; day_count INTEGER:=0;
BEGIN
 IF minutes IS NULL OR minutes<=0 OR minutes>525600 THEN RETURN NULL; END IF;
 IF COALESCE(cfg->'sla'->>'clock','business')='calendar' THEN RETURN start_at+minutes*INTERVAL '1 minute'; END IF;
 local_day:=(start_at AT TIME ZONE zone)::DATE;
 WHILE remaining>0 AND day_count<4000 LOOP
  IF COALESCE(cfg->'calendar'->'days','[1,2,3,4,5]'::jsonb) @> to_jsonb(ARRAY[EXTRACT(DOW FROM local_day)::INTEGER])
  AND NOT COALESCE(cfg->'calendar'->'holidays','[]'::jsonb) @> to_jsonb(ARRAY[local_day::TEXT]) THEN
   a:=(local_day+COALESCE(cfg->'calendar'->>'start','09:00')::TIME) AT TIME ZONE zone;
   b:=(local_day+COALESCE(cfg->'calendar'->>'end','18:00')::TIME) AT TIME ZONE zone;
   a:=GREATEST(a,cursor_at); available:=GREATEST(0,EXTRACT(EPOCH FROM b-a));
   IF available>=remaining THEN RETURN a+remaining*INTERVAL '1 second'; END IF;
   remaining:=remaining-available;
  END IF;
  local_day:=local_day+1;cursor_at:=local_day::TIMESTAMP AT TIME ZONE zone;day_count:=day_count+1;
 END LOOP;
 RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION flowops_set_ticket_deadlines() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE cfg JSONB;
BEGIN
 SELECT platform_config INTO cfg FROM app_settings WHERE tenant_id=NEW.tenant_id AND id=TRUE;
 cfg:=COALESCE(cfg,'{}'::jsonb);
 NEW.sla_policy_snapshot:=cfg;
 NEW.first_response_due_at:=flowops_business_deadline(NEW.created_at AT TIME ZONE 'UTC',COALESCE((cfg->'sla'->>'first_response_minutes')::INTEGER,0),cfg);
 NEW.resolution_due_at:=flowops_business_deadline(NEW.created_at AT TIME ZONE 'UTC',COALESCE((cfg->'sla'->>'resolution_minutes')::INTEGER,0),cfg);
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ticket_deadlines ON tickets;
CREATE TRIGGER ticket_deadlines BEFORE INSERT ON tickets FOR EACH ROW EXECUTE FUNCTION flowops_set_ticket_deadlines();

CREATE OR REPLACE FUNCTION flowops_capture_ticket_event() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE who TEXT:=COALESCE(NULLIF(current_setting('flowops.actor',TRUE),''),'system');
BEGIN
 IF TG_OP='INSERT' THEN
  INSERT INTO ticket_events(tenant_id,ticket_id,event_type,occurred_at,actor,payload) VALUES(NEW.tenant_id,NEW.id,'created',NEW.created_at AT TIME ZONE 'UTC',COALESCE(NULLIF(NEW.requester_id,''),who),jsonb_build_object('status',NEW.status,'department',NEW.department,'channel',NEW.channel));
 ELSE
  IF NEW.status IS DISTINCT FROM OLD.status THEN
   INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES(NEW.tenant_id,NEW.id,'status_changed',who,jsonb_build_object('from',OLD.status,'to',NEW.status));
   IF NEW.status IN ('closed','auto_resolved') AND OLD.status NOT IN ('closed','auto_resolved') THEN
    INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES(NEW.tenant_id,NEW.id,'closed',who,jsonb_build_object('status',NEW.status));
   ELSIF OLD.status IN ('closed','auto_resolved') AND NEW.status NOT IN ('closed','auto_resolved') THEN
    INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES(NEW.tenant_id,NEW.id,'reopened',who,jsonb_build_object('status',NEW.status));
   END IF;
  END IF;
  IF NEW.department IS DISTINCT FROM OLD.department THEN
   INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES(NEW.tenant_id,NEW.id,'department_changed',who,jsonb_build_object('from',OLD.department,'to',NEW.department));
  END IF;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ticket_process_events ON tickets;
CREATE TRIGGER ticket_process_events AFTER INSERT OR UPDATE OF status,department ON tickets FOR EACH ROW EXECUTE FUNCTION flowops_capture_ticket_event();
INSERT INTO ticket_events(tenant_id,ticket_id,event_type,occurred_at,actor,payload,origin)
SELECT tenant_id,id,'created',created_at AT TIME ZONE 'UTC','unknown',jsonb_build_object('status',status,'department',department),'reconstructed_creation'
FROM tickets ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION flowops_capture_work_event() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.running THEN INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor) VALUES(NEW.tenant_id,NEW.ticket_id,'work_started',NEW.operator_id); END IF;
 ELSIF NEW.running IS DISTINCT FROM OLD.running THEN
  INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES(NEW.tenant_id,NEW.ticket_id,CASE WHEN NEW.running THEN 'work_started' ELSE 'work_paused' END,NEW.operator_id,jsonb_build_object('active_seconds',NEW.active_seconds));
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS work_process_events ON ticket_work_sessions;
CREATE TRIGGER work_process_events AFTER INSERT OR UPDATE ON ticket_work_sessions FOR EACH ROW EXECUTE FUNCTION flowops_capture_work_event();
`
