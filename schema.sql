CREATE TABLE IF NOT EXISTS tickets (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    user_email VARCHAR(255),
    channel VARCHAR(50) DEFAULT 'web',
    subject TEXT,
    callback_url TEXT,
    raw_text TEXT NOT NULL,
    department VARCHAR(100) DEFAULT 'Unassigned',
    priority VARCHAR(50) DEFAULT 'medium',
    extracted_summary TEXT,
    rag_answer TEXT,
    operator_reply TEXT,
    pii_masked_count INTEGER NOT NULL DEFAULT 0,
    pii_masked_types TEXT[] NOT NULL DEFAULT '{}',
    status VARCHAR(50) DEFAULT 'new',
    delivery_status TEXT NOT NULL DEFAULT '',
    reply_version INTEGER NOT NULL DEFAULT 0,
    reply_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    event_type VARCHAR(64) NOT NULL,
    actor VARCHAR(128) NOT NULL,
    ticket_id INT NULL,
    details TEXT
);

CREATE TABLE IF NOT EXISTS ticket_deliveries (
    id BIGSERIAL PRIMARY KEY,
    ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    reply_version INTEGER NOT NULL,
    kind TEXT NOT NULL,
    channel TEXT NOT NULL DEFAULT '',
    recipient TEXT NOT NULL,
    body TEXT NOT NULL,
    dedupe_key TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','processing','sent','failed','superseded')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(ticket_id,reply_version,dedupe_key)
);
CREATE INDEX IF NOT EXISTS ticket_deliveries_ready ON ticket_deliveries(next_attempt_at) WHERE state IN ('pending','processing');

CREATE TABLE IF NOT EXISTS api_keys (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    name TEXT NOT NULL,
    key_prefix TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    scope TEXT NOT NULL DEFAULT 'ingest',
    allowed_origin TEXT NOT NULL DEFAULT '',
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS app_settings (
    tenant_id TEXT NOT NULL DEFAULT 'default',
    id BOOLEAN NOT NULL DEFAULT TRUE CHECK (id),
    widget_title TEXT NOT NULL DEFAULT 'Онлайн-поддержка',
    widget_color TEXT NOT NULL DEFAULT '#2563eb',
    widget_greeting TEXT NOT NULL DEFAULT 'Здравствуйте! Чем мы можем помочь?',
    builder_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    channel_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    channel_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (tenant_id,id)
);
CREATE TABLE IF NOT EXISTS channel_bindings (
    identity TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS workspaces (
    tenant_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO app_settings(tenant_id,id) VALUES ('default',TRUE) ON CONFLICT (tenant_id,id) DO NOTHING;

CREATE TABLE IF NOT EXISTS workspace_members (
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 display_name TEXT NOT NULL,
 email TEXT NOT NULL,
 role TEXT NOT NULL CHECK (role IN ('admin','operator','analyst','employee')),
 token_hash TEXT NOT NULL UNIQUE,
 is_active BOOLEAN NOT NULL DEFAULT TRUE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(tenant_id,email)
);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS requester_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS tickets_requester ON tickets(tenant_id,requester_id,created_at DESC);

ALTER TABLE workspace_members ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS workspace_members_login ON workspace_members(email) WHERE is_active=TRUE;
CREATE TABLE IF NOT EXISTS auth_sessions (
 token_hash TEXT PRIMARY KEY,
 csrf_hash TEXT NOT NULL,
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS auth_sessions_member ON auth_sessions(member_id);
CREATE INDEX IF NOT EXISTS auth_sessions_expiry ON auth_sessions(expires_at);

ALTER TABLE workspace_members ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS auth_version BIGINT NOT NULL DEFAULT 0;


CREATE TABLE IF NOT EXISTS bot_link_codes (
 token_hash TEXT PRIMARY KEY,
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 channel TEXT NOT NULL CHECK(channel IN ('telegram','max')),
 auth_version BIGINT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 UNIQUE(member_id,channel)
);
CREATE TABLE IF NOT EXISTS member_bot_links (
 bot_tenant_id TEXT NOT NULL,
 sender TEXT NOT NULL,
 channel TEXT NOT NULL CHECK(channel IN ('telegram','max')),
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 auth_version BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(bot_tenant_id,sender),
 UNIQUE(member_id,channel)
);
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS bot_tenant_id TEXT NOT NULL DEFAULT '';

-- Keyset pagination for operator queues and personal request history.
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS organization_type TEXT NOT NULL DEFAULT 'other';
CREATE INDEX IF NOT EXISTS tickets_tenant_cursor ON tickets(tenant_id,id DESC);
CREATE INDEX IF NOT EXISTS tickets_requester_cursor ON tickets(tenant_id,requester_id,id DESC);


CREATE TABLE IF NOT EXISTS ticket_work_sessions (
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 operator_id TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 active_seconds DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK(active_seconds>=0),
 running BOOLEAN NOT NULL DEFAULT FALSE,
 last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,operator_id,ticket_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ticket_work_one_active ON ticket_work_sessions(tenant_id,operator_id) WHERE running=TRUE;
CREATE INDEX IF NOT EXISTS ticket_work_totals ON ticket_work_sessions(tenant_id,ticket_id);


CREATE TABLE IF NOT EXISTS ticket_requests (
 tenant_id TEXT NOT NULL,
 scope_hash TEXT NOT NULL,
 key_hash TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,scope_hash,key_hash)
);
ALTER TABLE ticket_requests DROP CONSTRAINT IF EXISTS ticket_requests_tenant_id_fkey;
CREATE INDEX IF NOT EXISTS ticket_requests_ticket ON ticket_requests(ticket_id);

-- Persistent ticket conversations and answer ratings.

ALTER TABLE tickets ADD COLUMN IF NOT EXISTS requester_closed BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE IF NOT EXISTS ticket_messages (
 id BIGSERIAL PRIMARY KEY,
 tenant_id TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 role TEXT NOT NULL CHECK(role IN ('employee','assistant','operator','system')),
 actor TEXT NOT NULL DEFAULT '',
 body TEXT NOT NULL,
 source_key TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(tenant_id,ticket_id,source_key)
);
CREATE INDEX IF NOT EXISTS ticket_messages_page ON ticket_messages(tenant_id,ticket_id,id DESC);
CREATE TABLE IF NOT EXISTS ticket_answer_ratings (
 message_id BIGINT NOT NULL REFERENCES ticket_messages(id) ON DELETE CASCADE,
 actor TEXT NOT NULL,
 score INTEGER NOT NULL CHECK(score BETWEEN 1 AND 5),
 comment TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(message_id,actor)
);
CREATE TABLE IF NOT EXISTS bot_dialogues (
 bot_tenant_id TEXT NOT NULL,
 sender TEXT NOT NULL,
 ticket_id INTEGER REFERENCES tickets(id) ON DELETE CASCADE,
 PRIMARY KEY(bot_tenant_id,sender)
);
ALTER TABLE ticket_deliveries ADD COLUMN IF NOT EXISTS conversation_message_id BIGINT REFERENCES ticket_messages(id) ON DELETE CASCADE;
ALTER TABLE ticket_deliveries ADD COLUMN IF NOT EXISTS bot_route_tenant TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS bot_dialogue_receipts (
 bot_tenant_id TEXT NOT NULL,
 sender TEXT NOT NULL,
 key_hash TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 PRIMARY KEY(bot_tenant_id,sender,key_hash)
);
CREATE TABLE IF NOT EXISTS ticket_operator_reads (
 tenant_id TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 operator_id TEXT NOT NULL,
 last_message_id BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,ticket_id,operator_id)
);
CREATE TABLE IF NOT EXISTS ticket_queue_hidden (
 tenant_id TEXT NOT NULL,
 ticket_id INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 operator_id TEXT NOT NULL,
 last_employee_message_id BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,ticket_id,operator_id)
);


ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS platform_config JSONB NOT NULL DEFAULT '{}';
ALTER TABLE app_settings ADD COLUMN IF NOT EXISTS platform_revision BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS tickets_tenant_created ON tickets(tenant_id,created_at,id);
CREATE INDEX IF NOT EXISTS audit_ticket_event ON audit_logs(tenant_id,ticket_id,event_type,created_at);


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
DECLARE cfg JSONB; rule JSONB;
BEGIN
 SELECT platform_config INTO cfg FROM app_settings WHERE tenant_id=NEW.tenant_id AND id=TRUE;
 cfg:=COALESCE(cfg,'{}'::jsonb);
 FOR rule IN SELECT value FROM jsonb_array_elements(COALESCE(NULLIF(cfg->'process'->'sla','null'::jsonb),'[]'::jsonb)) LOOP
 IF flowops_rule_matches(rule,to_jsonb(NEW),cfg) THEN cfg:=cfg||jsonb_build_object('sla',rule->'policy','selected_sla_id',rule->>'id');
 IF rule ? 'calendar' THEN cfg:=jsonb_set(cfg,'{calendar}',rule->'calendar');END IF;
 IF NULLIF(rule->>'timezone','') IS NOT NULL THEN cfg:=jsonb_set(cfg,'{timezone}',rule->'timezone');END IF;
 IF rule ? 'pause_statuses' THEN cfg:=jsonb_set(cfg,'{process,pause_statuses}',rule->'pause_statuses');END IF;
 IF rule ? 'escalations' THEN cfg:=jsonb_set(cfg,'{process,escalations}',rule->'escalations');END IF;
 EXIT;END IF;
 END LOOP;
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


CREATE TABLE IF NOT EXISTS analytics_dashboards (
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 owner_id TEXT NOT NULL,
 name TEXT NOT NULL,
 shared BOOLEAN NOT NULL DEFAULT FALSE,
 revision BIGINT NOT NULL DEFAULT 1,
 config JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS analytics_dashboard_access ON analytics_dashboards(tenant_id,owner_id,shared);


CREATE TABLE IF NOT EXISTS optimization_decisions(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('accepted','rejected','deferred','implemented')),
 assignee_id TEXT NOT NULL DEFAULT '',
 note TEXT NOT NULL DEFAULT '',
 revision BIGINT NOT NULL DEFAULT 1,
 baseline JSONB NOT NULL,
 tasks JSONB NOT NULL DEFAULT '[]',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,id)
);


CREATE TABLE IF NOT EXISTS kb_articles(
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 author_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('draft','review','changes_requested','published','stale','archived')),
 revision BIGINT NOT NULL DEFAULT 1,
 published_revision BIGINT,
 document JSONB NOT NULL,
 approved_by TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS kb_article_versions(
 tenant_id TEXT NOT NULL,
 article_id TEXT NOT NULL REFERENCES kb_articles(id) ON DELETE CASCADE,
 revision BIGINT NOT NULL,
 state TEXT NOT NULL,
 document JSONB NOT NULL,
 actor TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(tenant_id,article_id,revision)
);
CREATE INDEX IF NOT EXISTS kb_article_list ON kb_articles(tenant_id,state,updated_at,id);

CREATE TABLE IF NOT EXISTS optimization_semantic_clusters(tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,id TEXT NOT NULL,payload JSONB NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),PRIMARY KEY(tenant_id,id));

ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS template_snapshot JSONB NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS tickets_search_vector ON tickets USING GIN(to_tsvector('russian',raw_text));


CREATE UNIQUE INDEX IF NOT EXISTS workspace_member_tenant_identity ON workspace_members(tenant_id,id);
CREATE TABLE IF NOT EXISTS organization_units(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 parent_id TEXT,
 name TEXT NOT NULL,
 kind TEXT NOT NULL DEFAULT 'department',
 routing_code TEXT NOT NULL DEFAULT '',
 active BOOLEAN NOT NULL DEFAULT TRUE,
 revision BIGINT NOT NULL DEFAULT 1,
 PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,parent_id) REFERENCES organization_units(tenant_id,id)
);
CREATE UNIQUE INDEX IF NOT EXISTS organization_unit_routing ON organization_units(tenant_id,routing_code) WHERE routing_code<>'';
CREATE INDEX IF NOT EXISTS organization_unit_parent ON organization_units(tenant_id,parent_id);
CREATE TABLE IF NOT EXISTS organization_unit_members(
 tenant_id TEXT NOT NULL,
 unit_id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 relation TEXT NOT NULL CHECK(relation IN ('member','manager','deputy','responsible')),
 PRIMARY KEY(tenant_id,unit_id,member_id,relation),
 FOREIGN KEY(tenant_id,unit_id) REFERENCES organization_units(tenant_id,id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES workspace_members(tenant_id,id)
);


CREATE UNIQUE INDEX IF NOT EXISTS ticket_first_response_observed ON ticket_events(tenant_id,ticket_id) WHERE event_type='response_available';
CREATE OR REPLACE FUNCTION flowops_capture_initial_response() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NEW.channel,'web') IN ('web','api') AND NULLIF(BTRIM(NEW.rag_answer),'') IS NOT NULL THEN
  INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) VALUES(NEW.tenant_id,NEW.id,'response_available','system',jsonb_build_object('channel',COALESCE(NEW.channel,'web'),'source','initial_answer')) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ticket_initial_response_observed ON tickets;
CREATE TRIGGER ticket_initial_response_observed AFTER INSERT ON tickets FOR EACH ROW EXECUTE FUNCTION flowops_capture_initial_response();
CREATE OR REPLACE FUNCTION flowops_capture_delivered_response() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state='sent' AND OLD.state IS DISTINCT FROM 'sent' AND NEW.kind='channel' THEN
  INSERT INTO ticket_events(tenant_id,ticket_id,event_type,actor,payload) SELECT tenant_id,id,'response_available','delivery',jsonb_build_object('channel',NEW.channel,'delivery_id',NEW.id) FROM tickets WHERE id=NEW.ticket_id ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS ticket_response_delivery_observed ON ticket_deliveries;
CREATE TRIGGER ticket_response_delivery_observed AFTER UPDATE OF state ON ticket_deliveries FOR EACH ROW EXECUTE FUNCTION flowops_capture_delivered_response();

CREATE TABLE IF NOT EXISTS organization_access_roles(
 tenant_id TEXT NOT NULL REFERENCES workspaces(tenant_id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 name TEXT NOT NULL,
 base_role TEXT NOT NULL CHECK(base_role IN ('operator','analyst','employee')),
 scope TEXT NOT NULL CHECK(scope IN ('own','organization','units')),
 unit_ids JSONB NOT NULL DEFAULT '[]',
 revision BIGINT NOT NULL DEFAULT 1,
 PRIMARY KEY(tenant_id,id)
);

CREATE TABLE IF NOT EXISTS organization_member_roles(
 tenant_id TEXT NOT NULL,
 member_id TEXT NOT NULL,
 role_id TEXT NOT NULL,
 PRIMARY KEY(tenant_id,member_id),
 FOREIGN KEY(tenant_id,member_id) REFERENCES workspace_members(tenant_id,id) ON DELETE CASCADE,
 FOREIGN KEY(tenant_id,role_id) REFERENCES organization_access_roles(tenant_id,id)
);
CREATE OR REPLACE FUNCTION flowops_ticket_visible(org TEXT,person TEXT,department_code TEXT,requester TEXT) RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
 WITH RECURSIVE policy AS (SELECT r.scope,r.unit_ids FROM organization_member_roles m JOIN organization_access_roles r ON r.tenant_id=m.tenant_id AND r.id=m.role_id WHERE m.tenant_id=org AND m.member_id=person), units AS(
 SELECT u.id,u.routing_code FROM organization_units u,policy p WHERE u.tenant_id=org AND u.active AND p.unit_ids @> to_jsonb(ARRAY[u.id])
 UNION SELECT child.id,child.routing_code FROM organization_units child JOIN units parent ON child.parent_id=parent.id WHERE child.tenant_id=org AND child.active
 ) SELECT NOT EXISTS(SELECT 1 FROM policy) OR requester=person OR EXISTS(SELECT 1 FROM policy WHERE scope='organization') OR EXISTS(SELECT 1 FROM policy WHERE scope='units') AND EXISTS(SELECT 1 FROM units WHERE routing_code<>'' AND routing_code=department_code)
$$;

ALTER TABLE organization_access_roles ADD COLUMN IF NOT EXISTS permissions JSONB;
ALTER TABLE organization_access_roles DROP CONSTRAINT IF EXISTS organization_access_roles_base_role_check;
ALTER TABLE organization_access_roles ADD CONSTRAINT organization_access_roles_base_role_check CHECK(base_role IN ('admin','operator','analyst','employee'));

ALTER TABLE organization_access_roles ADD COLUMN IF NOT EXISTS ticket_ids JSONB NOT NULL DEFAULT '[]';
ALTER TABLE organization_access_roles DROP CONSTRAINT IF EXISTS organization_access_roles_scope_check;
ALTER TABLE organization_access_roles ADD CONSTRAINT organization_access_roles_scope_check CHECK(scope IN ('own','organization','units','resources'));
CREATE OR REPLACE FUNCTION flowops_ticket_visible(org TEXT,person TEXT,department_code TEXT,requester TEXT,resource_id INTEGER) RETURNS BOOLEAN LANGUAGE sql STABLE AS $$
 SELECT flowops_ticket_visible(org,person,department_code,requester) OR EXISTS(SELECT 1 FROM organization_member_roles m JOIN organization_access_roles r ON r.tenant_id=m.tenant_id AND r.id=m.role_id WHERE m.tenant_id=org AND m.member_id=person AND r.scope='resources' AND r.ticket_ids @> to_jsonb(ARRAY[resource_id]))
$$;
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


CREATE TABLE IF NOT EXISTS onec_connections (
 tenant_id TEXT PRIMARY KEY,
 config JSONB NOT NULL,
 secret_ciphertext TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
