-- Work items' records (spec Sections 4.1 and 5): what the coordinator
-- decided, written before it acts, so that a restart resumes from them
-- (ADR-004). An item's records are numbered from 1 in commit order, and
-- recorded_at, the database's time of the commit, never goes back within an
-- item. A record names the item's incident as the item had it when the
-- record was written, and its run when it is about one ('' otherwise). Its
-- body is JSON with a version. Work records are audit records from their
-- first row: append-only, deleted only by the retention job, and chained
-- under their incident, so that an anchor names the incidents whose records
-- the chain gained (spec Section 9.4).
CREATE TABLE work_records (
    tenant_id   text        NOT NULL,
    item_id     text        NOT NULL,
    seq         bigint      NOT NULL CHECK (seq > 0),
    id          text        NOT NULL,
    incident_id text        NOT NULL CHECK (incident_id <> ''),
    run_id      text        NOT NULL,
    kind        text        NOT NULL,
    body        jsonb       NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, item_id, seq),
    UNIQUE (tenant_id, id)
);

-- A run's records, as a board reads its run's settings.
CREATE INDEX work_records_by_run ON work_records (tenant_id, run_id, kind) WHERE run_id <> '';

-- Records by kind, as a takeover finds the items that do not await their
-- close: one item_opened row per item, and no item_awaiting_close row.
CREATE INDEX work_records_by_kind ON work_records (tenant_id, kind, item_id);

CREATE TRIGGER work_records_append_only
    BEFORE UPDATE OR DELETE ON work_records
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER work_records_no_truncate
    BEFORE TRUNCATE ON work_records
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();

-- The table's key, and its columns in form 1, as migration 0005 renders the
-- other audited tables. Verification sends the same expressions itself.
CREATE FUNCTION audit_key(r work_records) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.item_id, r.seq)::text $$;

CREATE FUNCTION audit_form1(r work_records) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.item_id, r.seq, r.id, r.incident_id, r.run_id, r.kind, r.body,
                               (extract(epoch FROM r.recorded_at) * 1000000)::bigint)::text $$;

-- A work record's scope in the chain is its incident. Run ids hold no colon,
-- so no run's scope is an incident's.
CREATE FUNCTION audit_run(r work_records) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT 'incident:' || r.incident_id $$;

CREATE FUNCTION audit_chain_work_records() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'work_records', audit_key(NEW), audit_run(NEW), 1::smallint, audit_form1(NEW));
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
          AND (p.proname = 'audit_chain_work_records'
               OR p.proname IN ('audit_key', 'audit_form1', 'audit_run') AND p.proargtypes[0] = 'work_records'::regtype)
    LOOP
        EXECUTE format('ALTER FUNCTION %s SET search_path = pg_catalog, %I', f, current_schema());
    END LOOP;
END;
$$;

CREATE TRIGGER work_records_chain AFTER INSERT ON work_records
    FOR EACH ROW EXECUTE FUNCTION audit_chain_work_records();
