-- agentswarm:min_reader 8
-- Indexes that are not unique, on any table: they refuse no write.
CREATE INDEX leases_by_holder ON leases (holder);
CREATE INDEX IF NOT EXISTS work_records_by_incident ON work_records (tenant_id, incident_id) WHERE incident_id <> '';
