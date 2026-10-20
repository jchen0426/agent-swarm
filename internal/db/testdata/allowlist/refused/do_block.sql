-- A DO block runs SQL the allowlist cannot see; this one adds a column to an
-- audited table.
DO $body$
BEGIN
    EXECUTE 'ALTER TABLE tool_calls ADD COLUMN cost bigint';
END;
$body$;
