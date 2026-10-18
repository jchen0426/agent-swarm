-- Model calls the LLM gateway forwarded, refused or failed (spec Sections 9.3
-- and 9.4; ADR-007). A call's record and its request are committed before
-- the call is forwarded, and its outcome and completion are added when it
-- ends. Requests and completions are stored redacted, apart from the
-- records, so that the retention job can delete them after [30 days] while
-- the tokens and spend stay for the cost metrics (spec Section 13.6). A call
-- with no outcome has not finished, or its outcome was never recorded: it
-- counts at its estimate, its worst case. The three tables are audit records
-- from their first row: append-only, deleted only by the retention job, and
-- chained.
CREATE TABLE model_calls (
    tenant_id      text        NOT NULL,
    call_id        text        NOT NULL,
    run_id         text        NOT NULL,
    agent          text        NOT NULL,
    round          integer     NOT NULL CHECK (round >= 0),
    class          text        NOT NULL CHECK (class IN ('primary', 'shadow')),
    model          text        NOT NULL,
    request_bytes  bigint      NOT NULL CHECK (request_bytes >= 0),
    request_digest text        NOT NULL,
    estimate       bigint      NOT NULL CHECK (estimate >= 0),
    started_at     timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, call_id)
);

-- A run's spend is the sum of its calls' weighted tokens, and the day's the
-- sum of the tenant's calls' that began that day.
CREATE INDEX model_calls_by_run ON model_calls (tenant_id, run_id);
CREATE INDEX model_calls_by_day ON model_calls (tenant_id, started_at);

-- A call's request, as it was forwarded, and its completion, redacted.
CREATE TABLE model_call_contents (
    tenant_id text NOT NULL,
    call_id   text NOT NULL,
    part      text NOT NULL CHECK (part IN ('request', 'completion')),
    content   text NOT NULL,
    PRIMARY KEY (tenant_id, call_id, part),
    FOREIGN KEY (tenant_id, call_id) REFERENCES model_calls (tenant_id, call_id)
);

CREATE TABLE model_call_outcomes (
    tenant_id             text        NOT NULL,
    call_id               text        NOT NULL,
    status                text        NOT NULL CHECK (status IN ('ok', 'refused', 'error', 'unknown')),
    reason                text        NOT NULL,
    input_tokens          bigint      NOT NULL CHECK (input_tokens >= 0),
    cache_write_5m_tokens bigint      NOT NULL CHECK (cache_write_5m_tokens >= 0),
    cache_write_1h_tokens bigint      NOT NULL CHECK (cache_write_1h_tokens >= 0),
    cache_read_tokens     bigint      NOT NULL CHECK (cache_read_tokens >= 0),
    output_tokens         bigint      NOT NULL CHECK (output_tokens >= 0),
    spend                 bigint      NOT NULL CHECK (spend >= 0),
    weighted              bigint      NOT NULL CHECK (weighted >= 0),
    attempts              integer     NOT NULL CHECK (attempts >= 0),
    details               text        NOT NULL,
    ended_at              timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, call_id),
    FOREIGN KEY (tenant_id, call_id) REFERENCES model_calls (tenant_id, call_id)
);

CREATE TRIGGER model_calls_append_only
    BEFORE UPDATE OR DELETE ON model_calls
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_calls_no_truncate
    BEFORE TRUNCATE ON model_calls
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_call_contents_append_only
    BEFORE UPDATE OR DELETE ON model_call_contents
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_call_contents_no_truncate
    BEFORE TRUNCATE ON model_call_contents
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_call_outcomes_append_only
    BEFORE UPDATE OR DELETE ON model_call_outcomes
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER model_call_outcomes_no_truncate
    BEFORE TRUNCATE ON model_call_outcomes
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

-- Each table's key, and its columns in form 1, as migration 0005 renders
-- the other audited tables. Verification sends the same expressions itself.
CREATE FUNCTION audit_key(r model_calls) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id)::text $$;

CREATE FUNCTION audit_form1(r model_calls) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.run_id, r.agent, r.round, r.class, r.model, r.request_bytes,
                               r.request_digest, r.estimate, (extract(epoch FROM r.started_at) * 1000000)::bigint)::text $$;

CREATE FUNCTION audit_key(r model_call_contents) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id, r.part)::text $$;

CREATE FUNCTION audit_form1(r model_call_contents) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.part, r.content)::text $$;

CREATE FUNCTION audit_key(r model_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id)::text $$;

CREATE FUNCTION audit_form1(r model_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.status, r.reason, r.input_tokens, r.cache_write_5m_tokens,
                               r.cache_write_1h_tokens, r.cache_read_tokens, r.output_tokens, r.spend, r.weighted,
                               r.attempts, r.details, (extract(epoch FROM r.ended_at) * 1000000)::bigint)::text $$;

-- A content's run, and an outcome's, is its call's.
CREATE FUNCTION audit_run(r model_call_contents) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT c.run_id FROM model_calls c WHERE c.tenant_id = r.tenant_id AND c.call_id = r.call_id $$;

CREATE FUNCTION audit_run(r model_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT c.run_id FROM model_calls c WHERE c.tenant_id = r.tenant_id AND c.call_id = r.call_id $$;

CREATE FUNCTION audit_chain_model_calls() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'model_calls', audit_key(NEW), NEW.run_id, 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_model_call_contents() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'model_call_contents', audit_key(NEW), audit_run(NEW), 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_model_call_outcomes() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'model_call_outcomes', audit_key(NEW), audit_run(NEW), 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

-- Every function resolves names in the system catalog first, then in the
-- schema that holds these tables, whatever the caller's search path.
DO $$
DECLARE
    f regprocedure;
BEGIN
    FOR f IN
        SELECT p.oid::regprocedure FROM pg_proc p
        WHERE p.pronamespace = (SELECT n.oid FROM pg_namespace n WHERE n.nspname = current_schema())
          AND (p.proname IN ('audit_chain_model_calls', 'audit_chain_model_call_contents', 'audit_chain_model_call_outcomes')
               OR p.proname IN ('audit_key', 'audit_form1', 'audit_run')
                  AND p.proargtypes[0] IN ('model_calls'::regtype, 'model_call_contents'::regtype, 'model_call_outcomes'::regtype))
    LOOP
        EXECUTE format('ALTER FUNCTION %s SET search_path = pg_catalog, %I', f, current_schema());
    END LOOP;
END;
$$;

CREATE TRIGGER model_calls_chain AFTER INSERT ON model_calls
    FOR EACH ROW EXECUTE FUNCTION audit_chain_model_calls();

CREATE TRIGGER model_call_contents_chain AFTER INSERT ON model_call_contents
    FOR EACH ROW EXECUTE FUNCTION audit_chain_model_call_contents();

CREATE TRIGGER model_call_outcomes_chain AFTER INSERT ON model_call_outcomes
    FOR EACH ROW EXECUTE FUNCTION audit_chain_model_call_outcomes();
