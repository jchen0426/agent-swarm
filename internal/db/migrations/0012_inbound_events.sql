-- The inbound event store (spec Section 3.6; Plan 7c-1b, Decision 4): one row
-- for each event a source sent, keyed by the source and the event's id, with
-- what the event came to (its outcome and reason), the instance, dedup key and
-- correlation key it named, the status and time it reported, the item it
-- touched, and the event's title and payload, redacted and cut. The row's key
-- is how a repeated event is seen, and its instance and dedup-key indexes are
-- how an instance's last known status and the last item a key took are read.
-- Its rows are append-only evidence of the event logs class [30 days], deleted
-- only by the retention job (Plan 10a), and not chained: they decide nothing
-- that a work record does not record again. No build before this one reads the
-- table, and it comes with triggers, so this migration's readers are of its
-- own level.
CREATE TABLE inbound_events (
    tenant_id       text        NOT NULL,
    source          text        NOT NULL,
    event_id        text        NOT NULL CHECK (event_id <> ''),
    received_at     timestamptz NOT NULL,
    instance_id     text        NOT NULL,
    status          text        NOT NULL CHECK (status IN ('firing', 'resolved', '')),
    occurred_at     timestamptz,
    dedup_key       text        NOT NULL,
    correlation_key text        NOT NULL,
    item_id         text,
    applied         boolean     NOT NULL,
    outcome         text        NOT NULL CHECK (outcome IN ('opened', 'appended', 'late', 'resolved_unmatched', 'unroutable', 'refused')),
    reason          text        NOT NULL,
    title           text        NOT NULL,
    payload         text        NOT NULL,
    PRIMARY KEY (tenant_id, source, event_id),
    CHECK (applied = (outcome IN ('opened', 'appended', 'resolved_unmatched'))),
    CHECK (outcome NOT IN ('opened', 'appended') OR item_id IS NOT NULL),
    CHECK (outcome NOT IN ('resolved_unmatched', 'unroutable', 'refused') OR item_id IS NULL)
);

-- An instance's last known status (design D4, review O3).
CREATE INDEX inbound_events_by_instance ON inbound_events (tenant_id, source, instance_id, occurred_at DESC);

-- The last item a dedup key took (design D8).
CREATE INDEX inbound_events_by_dedup_key ON inbound_events (tenant_id, source, dedup_key, received_at DESC);

CREATE TRIGGER inbound_events_append_only
    BEFORE UPDATE OR DELETE ON inbound_events
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();

CREATE TRIGGER inbound_events_no_truncate
    BEFORE TRUNCATE ON inbound_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_guard();
