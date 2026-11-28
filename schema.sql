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
