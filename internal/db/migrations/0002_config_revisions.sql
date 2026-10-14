-- Configuration revisions the platform loaded, and the content hash of each
-- of their objects (spec Section 3.7). One commit has one content, so a
-- revision is recorded once.
CREATE TABLE config_revisions (
    tenant_id text        NOT NULL,
    commit_id text        NOT NULL,
    digest    text        NOT NULL,
    loaded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, commit_id)
);

CREATE TABLE config_objects (
    tenant_id text NOT NULL,
    commit_id text NOT NULL,
    kind      text NOT NULL,
    name      text NOT NULL,
    owner     text NOT NULL,
    file      text NOT NULL,
    hash      text NOT NULL,
    version   text NOT NULL,
    PRIMARY KEY (tenant_id, commit_id, kind, name),
    FOREIGN KEY (tenant_id, commit_id) REFERENCES config_revisions (tenant_id, commit_id)
);

-- Records never change. Only the retention job deletes them, and only after it
-- sets agentswarm.retention to 'on' for its own transaction.
CREATE FUNCTION config_records_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('agentswarm.retention', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END;
$$;

CREATE TRIGGER config_revisions_append_only
    BEFORE UPDATE OR DELETE ON config_revisions
    FOR EACH ROW EXECUTE FUNCTION config_records_guard();

CREATE TRIGGER config_revisions_no_truncate
    BEFORE TRUNCATE ON config_revisions
    FOR EACH STATEMENT EXECUTE FUNCTION config_records_guard();

CREATE TRIGGER config_objects_append_only
    BEFORE UPDATE OR DELETE ON config_objects
    FOR EACH ROW EXECUTE FUNCTION config_records_guard();

CREATE TRIGGER config_objects_no_truncate
    BEFORE TRUNCATE ON config_objects
    FOR EACH STATEMENT EXECUTE FUNCTION config_records_guard();
