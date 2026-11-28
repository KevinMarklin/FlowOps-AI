package database

const TicketFormsSchema = `
CREATE TABLE IF NOT EXISTS organization_ticket_forms(tenant_id TEXT PRIMARY KEY REFERENCES workspaces(tenant_id),revision BIGINT NOT NULL DEFAULT 1,definition JSONB NOT NULL DEFAULT '{"fields":[]}');
CREATE TABLE IF NOT EXISTS ticket_custom_fields(tenant_id TEXT NOT NULL,ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,revision BIGINT NOT NULL DEFAULT 1,values JSONB NOT NULL DEFAULT '{}',PRIMARY KEY(tenant_id,ticket_id));
CREATE TABLE IF NOT EXISTS ticket_attachments(id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,author_id TEXT NOT NULL,filename TEXT NOT NULL,mime TEXT NOT NULL,content BYTEA NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),CHECK(octet_length(content)<=5242880));
CREATE INDEX IF NOT EXISTS ticket_attachment_owner ON ticket_attachments(tenant_id,ticket_id);

CREATE TABLE IF NOT EXISTS organization_ai_usage(tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id),day DATE NOT NULL,calls INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(tenant_id,day));

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS form_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT '';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS ticket_type TEXT NOT NULL DEFAULT '';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS assignee_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS routing_rule_id TEXT NOT NULL DEFAULT '';
CREATE OR REPLACE FUNCTION flowops_apply_routing() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE cfg JSONB; builder JSONB; rule JSONB; destination TEXT; person TEXT;
BEGIN
 SELECT platform_config,builder_config INTO cfg,builder FROM app_settings WHERE tenant_id=NEW.tenant_id AND id=TRUE;
 FOR rule IN SELECT value FROM jsonb_array_elements(COALESCE(NULLIF(cfg->'process'->'routing','null'::jsonb),'[]'::jsonb)) WITH ORDINALITY e(value,n) ORDER BY COALESCE((value->>'order')::INTEGER,0),n LOOP
  IF flowops_rule_matches(rule,to_jsonb(NEW),cfg) THEN
   destination:=rule->>'department';
   IF builder ? 'departments' AND destination NOT IN ('General','Unassigned') AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF(builder->'departments','null'::jsonb),'[]'::jsonb)) d WHERE d->>'name'=destination) THEN CONTINUE; END IF;
   NEW.department:=destination;NEW.routing_rule_id:=rule->>'id';NEW.priority:=COALESCE(NULLIF(rule->>'priority',''),NEW.priority);
   person:=NULLIF(rule->>'assignee','');
   IF person IS NOT NULL AND EXISTS(SELECT 1 FROM workspace_members WHERE tenant_id=NEW.tenant_id AND id=person AND is_active AND role IN ('operator','admin') AND flowops_ticket_visible(NEW.tenant_id,id,NEW.department,NEW.requester_id,NEW.id)) THEN NEW.assignee_id:=person;
   ELSE SELECT m.id INTO person FROM workspace_members m WHERE m.tenant_id=NEW.tenant_id AND m.is_active AND m.role='operator' AND flowops_ticket_visible(NEW.tenant_id,m.id,NEW.department,NEW.requester_id,NEW.id) ORDER BY (SELECT COUNT(*) FROM tickets t WHERE t.tenant_id=m.tenant_id AND t.assignee_id=m.id AND t.status NOT IN ('closed','auto_resolved')),m.id LIMIT 1;NEW.assignee_id:=COALESCE(person,'');END IF;
   EXIT;
  END IF;
 END LOOP;
 IF COALESCE(NEW.department,'') IN ('','Unassigned') AND NULLIF(cfg->'process'->>'fallback_department','') IS NOT NULL AND (NOT COALESCE(builder ? 'departments',false) OR cfg->'process'->>'fallback_department' IN ('General','Unassigned') OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF(builder->'departments','null'::jsonb),'[]'::jsonb)) d WHERE d->>'name'=cfg->'process'->>'fallback_department')) THEN NEW.department:=cfg->'process'->>'fallback_department';END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS a_ticket_routing ON tickets;
CREATE TRIGGER a_ticket_routing BEFORE INSERT ON tickets FOR EACH ROW EXECUTE FUNCTION flowops_apply_routing();

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS sla_paused_at TIMESTAMPTZ;
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS sla_pause_seconds DOUBLE PRECISION NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS ticket_sla_notifications(
 id BIGSERIAL PRIMARY KEY,tenant_id TEXT NOT NULL,ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 kind TEXT NOT NULL,level_id TEXT NOT NULL DEFAULT '',recipient_id TEXT NOT NULL DEFAULT '',created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),read_at TIMESTAMPTZ,
 UNIQUE(tenant_id,ticket_id,kind,level_id)
);
CREATE OR REPLACE FUNCTION flowops_sla_pause() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE paused BOOLEAN; seconds DOUBLE PRECISION; cfg JSONB; zone TEXT; day DATE; a TIMESTAMPTZ; b TIMESTAMPTZ;
BEGIN
 cfg:=NEW.sla_policy_snapshot;
 paused:=COALESCE(NULLIF(cfg->'process'->'pause_statuses','null'::jsonb),'[]'::jsonb) @> to_jsonb(ARRAY[NEW.status]);
 IF paused AND NEW.sla_paused_at IS NULL THEN NEW.sla_paused_at:=NOW();
 ELSIF NOT paused AND NEW.sla_paused_at IS NOT NULL THEN
  seconds:=0;
  IF cfg->'sla'->>'clock'='business' THEN
   zone:=COALESCE(cfg->>'timezone','Europe/Moscow');
   FOR day IN SELECT generate_series((NEW.sla_paused_at AT TIME ZONE zone)::DATE,(NOW() AT TIME ZONE zone)::DATE,INTERVAL '1 day')::DATE LOOP
    IF COALESCE(cfg->'calendar'->'days','[1,2,3,4,5]') @> to_jsonb(ARRAY[EXTRACT(DOW FROM day)::INTEGER]) AND NOT COALESCE(NULLIF(cfg->'calendar'->'holidays','null'::jsonb),'[]'::jsonb) @> to_jsonb(ARRAY[day::TEXT]) THEN
     a:=GREATEST(NEW.sla_paused_at,(day+COALESCE(cfg->'calendar'->>'start','09:00')::TIME) AT TIME ZONE zone);
     b:=LEAST(NOW(),(day+COALESCE(cfg->'calendar'->>'end','18:00')::TIME) AT TIME ZONE zone);seconds:=seconds+GREATEST(0,EXTRACT(EPOCH FROM b-a));
    END IF;
   END LOOP;
  ELSE seconds:=EXTRACT(EPOCH FROM NOW()-NEW.sla_paused_at);END IF;
  NEW.sla_pause_seconds:=OLD.sla_pause_seconds+seconds;
  IF seconds>0 AND NEW.first_response_due_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM ticket_events e WHERE e.tenant_id=NEW.tenant_id AND e.ticket_id=NEW.id AND e.event_type='response_available' AND e.origin='observed') AND NOT EXISTS(SELECT 1 FROM ticket_messages m WHERE m.tenant_id=NEW.tenant_id AND m.ticket_id=NEW.id AND m.role IN ('operator','assistant') AND m.source_key NOT IN ('legacy-reply','initial-answer')) THEN NEW.first_response_due_at:=flowops_business_deadline(NEW.first_response_due_at,CEIL(seconds/60)::INTEGER,cfg);END IF;
  IF seconds>0 AND NEW.resolution_due_at IS NOT NULL THEN NEW.resolution_due_at:=flowops_business_deadline(NEW.resolution_due_at,CEIL(seconds/60)::INTEGER,cfg);END IF;
  NEW.sla_paused_at:=NULL;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS b_ticket_sla_pause ON tickets;
DROP TRIGGER IF EXISTS z_ticket_sla_pause ON tickets;
CREATE TRIGGER z_ticket_sla_pause BEFORE INSERT OR UPDATE OF status ON tickets FOR EACH ROW EXECUTE FUNCTION flowops_sla_pause();

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS custom_priority TEXT NOT NULL DEFAULT '';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS workflow_status TEXT NOT NULL DEFAULT '';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS tags JSONB NOT NULL DEFAULT '[]';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS custom_data JSONB NOT NULL DEFAULT '{}';
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS form_snapshot JSONB NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS organization_units_parent ON organization_units(tenant_id,parent_id);
CREATE INDEX IF NOT EXISTS ticket_assignee_open ON tickets(tenant_id,assignee_id) WHERE status NOT IN ('closed','auto_resolved');
CREATE OR REPLACE FUNCTION flowops_rule_matches(rule JSONB,data JSONB,cfg JSONB) RETURNS BOOLEAN LANGUAGE plpgsql STABLE AS $$
DECLARE m RECORD; actual TEXT; membership BOOLEAN; zone TEXT; local_at TIMESTAMP; working BOOLEAN; load_count INTEGER;
BEGIN
 FOR m IN SELECT key,value FROM jsonb_each_text(COALESCE(NULLIF(rule->'match','null'::jsonb),'{}'::jsonb)) LOOP
  actual:=NULL;
  IF m.key='channel' AND m.value IN ('telegram','max') THEN actual:=CASE WHEN data->>'channel'='bot' AND data->>'user_email' LIKE 'tg:%' THEN 'telegram' WHEN data->>'channel'='bot' AND data->>'user_email' LIKE 'max:%' THEN 'max' ELSE data->>'channel' END;
  ELSIF m.key='requester_role' THEN SELECT role INTO actual FROM workspace_members WHERE tenant_id=data->>'tenant_id' AND id=data->>'requester_id' AND is_active;
  ELSIF m.key IN ('requester_unit','branch') THEN
   WITH RECURSIVE units AS(SELECT id FROM organization_units WHERE tenant_id=data->>'tenant_id' AND id=m.value AND active UNION SELECT c.id FROM organization_units c JOIN units p ON c.parent_id=p.id WHERE c.tenant_id=data->>'tenant_id' AND c.active)
   SELECT EXISTS(SELECT 1 FROM organization_unit_members mem WHERE mem.tenant_id=data->>'tenant_id' AND mem.member_id=data->>'requester_id' AND mem.unit_id IN(SELECT id FROM units)) INTO membership;
   IF NOT membership THEN RETURN FALSE;END IF;CONTINUE;
  ELSIF m.key='working_time' THEN
   zone:=COALESCE(cfg->>'timezone','Europe/Moscow');local_at:=((data->>'created_at')::TIMESTAMP AT TIME ZONE 'UTC') AT TIME ZONE zone;
   working:=COALESCE(cfg->'calendar'->'days','[1,2,3,4,5]') @> to_jsonb(ARRAY[EXTRACT(DOW FROM local_at)::INTEGER]) AND NOT COALESCE(NULLIF(cfg->'calendar'->'holidays','null'::jsonb),'[]'::jsonb) @> to_jsonb(ARRAY[local_at::DATE::TEXT]) AND local_at::TIME>=COALESCE(cfg->'calendar'->>'start','09:00')::TIME AND local_at::TIME<COALESCE(cfg->'calendar'->>'end','18:00')::TIME;
   actual:=working::TEXT;
  ELSIF m.key='max_open' THEN
   SELECT COUNT(*) INTO load_count FROM tickets t WHERE t.tenant_id=data->>'tenant_id' AND t.status NOT IN ('closed','auto_resolved') AND (CASE WHEN NULLIF(rule->>'assignee','') IS NOT NULL THEN t.assignee_id=rule->>'assignee' ELSE t.department=COALESCE(rule->>'department',data->>'department') END);
   IF load_count>m.value::INTEGER THEN RETURN FALSE;END IF;CONTINUE;
  ELSIF LEFT(m.key,6)='field.' THEN
   IF jsonb_typeof(data->'custom_data'->SUBSTRING(m.key FROM 7))='array' THEN IF NOT (data->'custom_data'->SUBSTRING(m.key FROM 7)) @> to_jsonb(ARRAY[m.value]) THEN RETURN FALSE;END IF;CONTINUE;
   ELSE actual:=data->'custom_data'->>SUBSTRING(m.key FROM 7);END IF;
  ELSE actual:=data->>m.key;
  END IF;
  IF actual IS NULL OR actual<>m.value THEN RETURN FALSE;END IF;
 END LOOP;
 RETURN TRUE;
END $$;

CREATE OR REPLACE FUNCTION flowops_require_completed_form() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE field JSONB; value JSONB;
BEGIN
 IF NEW.status IN ('closed','auto_resolved') AND OLD.status NOT IN ('closed','auto_resolved') THEN
  FOR field IN SELECT v FROM jsonb_array_elements(COALESCE(NULLIF(NEW.form_snapshot->'fields','null'::jsonb),'[]'::jsonb)) v LOOP
   IF COALESCE((field->>'required')::BOOLEAN,FALSE) AND (COALESCE(field->>'show_when','')='' OR NEW.custom_data->>(field->>'show_when')=field->>'equals') THEN
    value:=NEW.custom_data->(field->>'id');
    IF value IS NULL OR value IN ('null'::jsonb,'""'::jsonb,'[]'::jsonb) OR field->>'type'='attachment' AND NOT EXISTS(SELECT 1 FROM ticket_attachments a WHERE a.tenant_id=NEW.tenant_id AND a.ticket_id=NEW.id AND a.id=NEW.custom_data->>(field->>'id')) THEN
     RAISE EXCEPTION 'Complete required ticket fields before closing' USING ERRCODE='23514',CONSTRAINT='ticket_form_complete';
    END IF;
   END IF;
  END LOOP;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS zz_ticket_form_completion ON tickets;
CREATE TRIGGER zz_ticket_form_completion BEFORE UPDATE OF status ON tickets FOR EACH ROW EXECUTE FUNCTION flowops_require_completed_form();
`
