-- A tool call's outcome names the side its failure was on (Plan 7d-1,
-- Decision 20): the server, the platform, the tool's own error, or the agent;
-- '' for a call that did not fail, or that its round's end or its run's stop
-- cut short. The coordinator counts a round's failed calls by it. Outcomes
-- are chained in form 2 from here on, which adds the failure to form 1's
-- columns; those chained before stay in form 1, and verification renders
-- each record in its own form. A build older than this one renders no form
-- 2, so this migration's readers are of its own level.
ALTER TABLE tool_call_outcomes ADD COLUMN failure text NOT NULL DEFAULT ''
    CHECK (failure IN ('', 'server', 'platform', 'tool', 'agent'));

-- A round's tool calls, as the coordinator counts the failed ones.
CREATE INDEX tool_calls_by_round ON tool_calls (tenant_id, run_id, round);

-- An outcome's columns in form 2: form 1's, then the failure. Verification
-- sends the same expression itself.
CREATE FUNCTION audit_form2(r tool_call_outcomes) RETURNS text LANGUAGE sql STABLE
AS $$ SELECT jsonb_build_array(r.tenant_id, r.call_id, r.status, r.output,
                               (extract(epoch FROM r.ended_at) * 1000000)::bigint, r.failure)::text $$;

CREATE OR REPLACE FUNCTION audit_chain_tool_call_outcomes() RETURNS trigger LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM audit_chain_append(NEW.tenant_id, 'tool_call_outcomes', audit_key(NEW), audit_run(NEW), 2::smallint, audit_form2(NEW));
    RETURN NULL;
END;
$$;

-- CREATE OR REPLACE FUNCTION drops a function's settings, so every function
-- this migration creates or replaces resolves names in the system catalog
-- first, then in the schema that holds these tables, as migration 0005 has
-- every chain function do, whatever the caller's search path.
DO $$
DECLARE
    f regprocedure;
BEGIN
    FOR f IN
        SELECT p.oid::regprocedure FROM pg_proc p
        WHERE p.pronamespace = (SELECT n.oid FROM pg_namespace n WHERE n.nspname = current_schema())
          AND (p.proname = 'audit_chain_tool_call_outcomes'
               OR p.proname = 'audit_form2' AND p.proargtypes[0] = 'tool_call_outcomes'::regtype)
    LOOP
        EXECUTE format('ALTER FUNCTION %s SET search_path = pg_catalog, %I', f, current_schema());
    END LOOP;
END;
$$;
