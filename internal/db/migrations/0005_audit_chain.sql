-- The audit chain (spec Section 9.4). Every row of the audited tables is
-- chained, in the transaction that inserts it, after the row chained before
-- it in its tenant. A record names the row by its source table and key,
-- holds its run, the form of its digest, the digest of the row's columns as
-- the database renders them, and the hash that links it to the record
-- before. Verification renders the rows again and recomputes the hashes
-- outside the database; anchors outside it hold the chain's head.
CREATE TABLE audit_chain (
    tenant_id  text        NOT NULL,
    seq        bigint      NOT NULL CHECK (seq > 0),
    source     text        NOT NULL,
    key        text        NOT NULL,
    run_id     text        NOT NULL,
    form       smallint    NOT NULL CHECK (form > 0),
    digest     bytea       NOT NULL CHECK (octet_length(digest) = 32),
    prev       bytea       NOT NULL CHECK (octet_length(prev) = 32),
    hash       bytea       NOT NULL CHECK (octet_length(hash) = 32),
    chained_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, seq),
    UNIQUE (tenant_id, source, key)
);

-- Records never change and are never deleted, by the retention job either:
-- pruning comes with chained prune records (Plan 10), and until then a chain
-- that does not start at its first record shows a deletion.
CREATE FUNCTION audit_chain_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_chain is append-only';
END;
$$;

CREATE TRIGGER audit_chain_append_only
    BEFORE UPDATE OR DELETE ON audit_chain
    FOR EACH ROW EXECUTE FUNCTION audit_chain_guard();

CREATE TRIGGER audit_chain_no_truncate
    BEFORE TRUNCATE ON audit_chain
    FOR EACH STATEMENT EXECUTE FUNCTION audit_chain_guard();

-- audit_chain_append chains one row. It takes the tenant's chain lock and
-- holds it until the transaction ends, so records commit in the order of
-- their seq, and a rollback takes its records with it and leaves no gap.
-- The chaining time is read once the lock is held, so it never goes back.
-- That holds only at read committed, where the statement after the lock
-- sees the record of the transaction it waited for; at any other level the
-- insert fails. A transaction takes the chain lock after any other lock it
-- holds, the run's lock of the board among them, so the two never deadlock.
-- The hash covers the hash before, the seq, the hashes of the source, the
-- key and the run, the form, the digest and the chaining time in
-- microseconds.
CREATE FUNCTION audit_chain_append(p_tenant text, p_source text, p_key text, p_run text, p_form smallint, p_rendered text)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    v_seq    bigint;
    v_prev   bytea;
    v_digest bytea := sha256(convert_to(p_rendered, 'UTF8'));
    v_at     timestamptz;
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION 'audited rows are inserted at read committed, not %', current_setting('transaction_isolation');
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('audit_chain/' || p_tenant, 0));
    v_at := clock_timestamp();
    SELECT c.seq, c.hash INTO v_seq, v_prev
    FROM audit_chain c WHERE c.tenant_id = p_tenant ORDER BY c.seq DESC LIMIT 1;
    IF NOT FOUND THEN
        v_seq := 0;
        v_prev := decode(repeat('00', 32), 'hex');
    END IF;
    v_seq := v_seq + 1;
    INSERT INTO audit_chain (tenant_id, seq, source, key, run_id, form, digest, prev, hash, chained_at)
    VALUES (p_tenant, v_seq, p_source, p_key, p_run, p_form, v_digest, v_prev,
            sha256(v_prev || int8send(v_seq)
                   || sha256(convert_to(p_source, 'UTF8')) || sha256(convert_to(p_key, 'UTF8'))
                   || sha256(convert_to(p_run, 'UTF8')) || int2send(p_form) || v_digest
                   || int8send((extract(epoch FROM v_at) * 1000000)::bigint)),
            v_at);
END;
$$;

-- Each audited table's key, and its columns in form 1: one JSON array, times
-- as microseconds since the epoch, so that the session's settings never
-- change the text. Verification sends the same expressions itself.
CREATE FUNCTION audit_key(r board_events) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.run_id, r.seq)::text $$;

CREATE FUNCTION audit_form1(r board_events) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.run_id, r.seq, r.id, r.round, r.kind, r.author_kind, r.author_name,
                               (extract(epoch FROM r.committed_at) * 1000000)::bigint, r.body)::text $$;

CREATE FUNCTION audit_key(r tool_calls) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id)::text $$;

CREATE FUNCTION audit_form1(r tool_calls) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.run_id, r.agent, r.server, r.tool, r.schema_hash, r.arguments, r.round,
                               (extract(epoch FROM r.started_at) * 1000000)::bigint, r.cause)::text $$;

CREATE FUNCTION audit_key(r tool_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.call_id)::text $$;

CREATE FUNCTION audit_form1(r tool_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.status, r.output,
                               (extract(epoch FROM r.ended_at) * 1000000)::bigint)::text $$;

CREATE FUNCTION audit_key(r tool_hides) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.endpoint, r.tool, r.pinned_hash, r.registration)::text $$;

CREATE FUNCTION audit_form1(r tool_hides) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.endpoint, r.tool, r.pinned_hash, r.registration, r.server, r.reason,
                               r.observed_hash, (extract(epoch FROM r.hidden_at) * 1000000)::bigint)::text $$;

CREATE FUNCTION audit_key(r config_revisions) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.commit_id)::text $$;

CREATE FUNCTION audit_form1(r config_revisions) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.commit_id, r.digest, (extract(epoch FROM r.loaded_at) * 1000000)::bigint)::text $$;

CREATE FUNCTION audit_key(r config_objects) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.commit_id, r.kind, r.name)::text $$;

CREATE FUNCTION audit_form1(r config_objects) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.commit_id, r.kind, r.name, r.owner, r.file, r.hash, r.version)::text $$;

-- An outcome's run is its call's.
CREATE FUNCTION audit_run(r tool_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT c.run_id FROM tool_calls c WHERE c.tenant_id = r.tenant_id AND c.call_id = r.call_id $$;

CREATE FUNCTION audit_chain_board_events() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'board_events', audit_key(NEW), NEW.run_id, 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_tool_calls() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'tool_calls', audit_key(NEW), NEW.run_id, 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_tool_call_outcomes() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'tool_call_outcomes', audit_key(NEW), audit_run(NEW), 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_tool_hides() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'tool_hides', audit_key(NEW), '', 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_config_revisions() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'config_revisions', audit_key(NEW), '', 1::smallint, audit_form1(NEW));
    RETURN NULL;
END;
$$;

CREATE FUNCTION audit_chain_config_objects() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'config_objects', audit_key(NEW), '', 1::smallint, audit_form1(NEW));
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
          AND (p.proname LIKE 'audit\_chain%' OR p.proname IN ('audit_key', 'audit_form1', 'audit_run'))
    LOOP
        EXECUTE format('ALTER FUNCTION %s SET search_path = pg_catalog, %I', f, current_schema());
    END LOOP;
END;
$$;

-- Rows that exist now are chained first, in the order they were written.
-- The audited tables are locked against writes until the migration commits,
-- so no row is written between the rows chained here and the triggers.
LOCK TABLE board_events, tool_calls, tool_call_outcomes, tool_hides, config_revisions, config_objects
    IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT * FROM (
            SELECT tenant_id, 'board_events' AS source, audit_key(b) AS key, run_id, audit_form1(b) AS rendered, committed_at AS at FROM board_events b
            UNION ALL
            SELECT tenant_id, 'tool_calls', audit_key(c), run_id, audit_form1(c), started_at FROM tool_calls c
            UNION ALL
            SELECT tenant_id, 'tool_call_outcomes', audit_key(o), audit_run(o), audit_form1(o), ended_at FROM tool_call_outcomes o
            UNION ALL
            SELECT tenant_id, 'tool_hides', audit_key(h), '', audit_form1(h), hidden_at FROM tool_hides h
            UNION ALL
            SELECT tenant_id, 'config_revisions', audit_key(v), '', audit_form1(v), loaded_at FROM config_revisions v
            UNION ALL
            SELECT o.tenant_id, 'config_objects', audit_key(o), '', audit_form1(o), v.loaded_at
            FROM config_objects o JOIN config_revisions v USING (tenant_id, commit_id)
        ) rows
        ORDER BY at, source, key
    LOOP
        PERFORM audit_chain_append(r.tenant_id, r.source, r.key, r.run_id, 1::smallint, r.rendered);
    END LOOP;
END;
$$;

CREATE TRIGGER board_events_chain AFTER INSERT ON board_events
    FOR EACH ROW EXECUTE FUNCTION audit_chain_board_events();

CREATE TRIGGER tool_calls_chain AFTER INSERT ON tool_calls
    FOR EACH ROW EXECUTE FUNCTION audit_chain_tool_calls();

CREATE TRIGGER tool_call_outcomes_chain AFTER INSERT ON tool_call_outcomes
    FOR EACH ROW EXECUTE FUNCTION audit_chain_tool_call_outcomes();

CREATE TRIGGER tool_hides_chain AFTER INSERT ON tool_hides
    FOR EACH ROW EXECUTE FUNCTION audit_chain_tool_hides();

CREATE TRIGGER config_revisions_chain AFTER INSERT ON config_revisions
    FOR EACH ROW EXECUTE FUNCTION audit_chain_config_revisions();

CREATE TRIGGER config_objects_chain AFTER INSERT ON config_objects
    FOR EACH ROW EXECUTE FUNCTION audit_chain_config_objects();

-- Verification joins the chain with every audited table; it needs the
-- chain's statistics from the start.
ANALYZE audit_chain;
