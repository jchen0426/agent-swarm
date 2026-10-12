-- Board events: the append-only log of every run's blackboard (spec Section 4.2).
CREATE TABLE board_events (
    tenant_id    text        NOT NULL,
    run_id       text        NOT NULL,
    seq          bigint      NOT NULL CHECK (seq > 0),
    id           text        NOT NULL,
    round        integer     NOT NULL CHECK (round >= 0),
    kind         text        NOT NULL,
    author_kind  text        NOT NULL,
    author_name  text        NOT NULL,
    committed_at timestamptz NOT NULL,
    body         jsonb       NOT NULL,
    PRIMARY KEY (tenant_id, run_id, seq),
    UNIQUE (tenant_id, id)
);

-- Entries never change. Only the retention job deletes them, and only after it
-- sets agentswarm.retention to 'on' for its own transaction.
CREATE FUNCTION board_events_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('agentswarm.retention', true) = 'on' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'board_events is append-only';
END;
$$;

CREATE TRIGGER board_events_append_only
    BEFORE UPDATE OR DELETE ON board_events
    FOR EACH ROW EXECUTE FUNCTION board_events_guard();

CREATE TRIGGER board_events_no_truncate
    BEFORE TRUNCATE ON board_events
    FOR EACH STATEMENT EXECUTE FUNCTION board_events_guard();
