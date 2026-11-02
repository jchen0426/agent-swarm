-- The kill switch (spec Section 8.3; Plan 7b-1, Decision 5): one row each
-- time it is turned on or off, numbered from 1 for each tenant, with who
-- turned it, the reason's code, its text redacted ('' when none), and the
-- database's time. Its state is its latest row, and no row means off. Its
-- rows are audit records from the first: append-only, deleted only by the
-- retention job, and chained with the scope '', as configuration revisions
-- are, since they belong to no run and no incident. No build before this one
-- reads the table, and it comes with triggers, so this migration's readers
-- are of its own level.
CREATE TABLE kill_switch (
    tenant_id   text        NOT NULL,
    seq         bigint      NOT NULL CHECK (seq > 0),
    id          text        NOT NULL,
    state       text        NOT NULL CHECK (state IN ('on', 'off')),
    actor       text        NOT NULL,
    reason_code text        NOT NULL,
    reason_text text        NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, seq),
    UNIQUE (tenant_id, id)
);

CREATE TRIGGER kill_switch_append_only
    BEFORE UPDATE OR DELETE ON kill_switch
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER kill_switch_no_truncate
    BEFORE TRUNCATE ON kill_switch
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

-- The table's key, and its columns in form 1, as migration 0007 renders
-- work_records. Verification sends the same expressions itself.
CREATE FUNCTION audit_key(r kill_switch) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.seq)::text $$;

CREATE FUNCTION audit_form1(r kill_switch) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.seq, r.id, r.state, r.actor, r.reason_code, r.reason_text,
                               (extract(epoch FROM r.recorded_at) * 1000000)::bigint)::text $$;

CREATE FUNCTION audit_chain_kill_switch() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'kill_switch', audit_key(NEW), '', 1::smallint, audit_form1(NEW));
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
          AND (p.proname = 'audit_chain_kill_switch'
               OR p.proname IN ('audit_key', 'audit_form1') AND p.proargtypes[0] = 'kill_switch'::regtype)
    LOOP
        EXECUTE format('ALTER FUNCTION %s SET search_path = pg_catalog, %I', f, current_schema());
    END LOOP;
END;
$$;

CREATE TRIGGER kill_switch_chain AFTER INSERT ON kill_switch
    FOR EACH ROW EXECUTE FUNCTION audit_chain_kill_switch();
