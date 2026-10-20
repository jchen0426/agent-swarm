-- A chain form is a function: replacing one changes what the chain covers.
CREATE OR REPLACE FUNCTION audit_form1(r tool_call_outcomes) RETURNS text LANGUAGE plpgsql STABLE
AS $$
BEGIN
    RETURN jsonb_build_array(r.tenant_id, r.call_id, r.status)::text;
END;
$$;
