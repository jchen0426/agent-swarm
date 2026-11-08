-- An incident's items (Plan 7c-1a, Decision 18): an item's incident is its
-- latest record's, so the items of an incident are read by the incident and
-- then each item's latest record. This build writes records no build before
-- it folds (run_extended, and the links and unlinks after it), so this
-- migration declares no older reader: its readers are of its own level, and
-- no build of 7b-1 meets those records (Contract C17).
CREATE INDEX work_records_by_incident ON work_records (tenant_id, incident_id, item_id);
