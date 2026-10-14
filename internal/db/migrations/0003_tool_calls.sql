-- Tool calls the MCP gateway forwarded, refused or failed (spec Sections 6.2
-- and 9.4). A call's record is committed before the call is forwarded, and
-- its outcome is added when it ends. Arguments and outputs are stored
-- redacted. A call with no outcome never finished.
CREATE TABLE tool_calls (
    tenant_id   text        NOT NULL,
    call_id     text        NOT NULL,
    run_id      text        NOT NULL,
    agent       text        NOT NULL,
    server      text        NOT NULL,
    tool        text        NOT NULL,
    schema_hash text        NOT NULL,
    arguments   text        NOT NULL,
    round       integer     NOT NULL CHECK (round >= 0),
    started_at  timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, call_id)
);

CREATE TABLE tool_call_outcomes (
    tenant_id text        NOT NULL,
    call_id   text        NOT NULL,
    status    text        NOT NULL CHECK (status IN ('ok', 'read_error', 'refused')),
    output    text        NOT NULL,
    ended_at  timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, call_id),
    FOREIGN KEY (tenant_id, call_id) REFERENCES tool_calls (tenant_id, call_id)
);

-- Pinned tool definitions the gateway hid, because a server offered another
-- definition or one the gateway cannot use (spec Section 3.3). A hide lasts
-- until the registration is reviewed again, which gives the registration
-- another hash. Each row is also the notice its owner is sent.
CREATE TABLE tool_hides (
    tenant_id     text        NOT NULL,
    endpoint      text        NOT NULL,
    tool          text        NOT NULL,
    pinned_hash   text        NOT NULL,
    registration  text        NOT NULL,
    server        text        NOT NULL,
    reason        text        NOT NULL CHECK (reason IN ('changed', 'hidden_characters', 'schema')),
    observed_hash text        NOT NULL,
    hidden_at     timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, endpoint, tool, pinned_hash, registration)
);

-- Records never change. Only the retention job deletes them, and only after it
-- sets agentswarm.retention to 'on' for its own transaction.
CREATE FUNCTION audit_records_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('agentswarm.retention', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END;
$$;

CREATE TRIGGER tool_calls_append_only
    BEFORE UPDATE OR DELETE ON tool_calls
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER tool_calls_no_truncate
    BEFORE TRUNCATE ON tool_calls
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER tool_call_outcomes_append_only
    BEFORE UPDATE OR DELETE ON tool_call_outcomes
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER tool_call_outcomes_no_truncate
    BEFORE TRUNCATE ON tool_call_outcomes
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER tool_hides_append_only
    BEFORE UPDATE OR DELETE ON tool_hides
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER tool_hides_no_truncate
    BEFORE TRUNCATE ON tool_hides
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();
