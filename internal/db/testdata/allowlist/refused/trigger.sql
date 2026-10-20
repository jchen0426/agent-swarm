-- A trigger acts on what an older build writes.
CREATE TRIGGER leases_no_delete
    BEFORE DELETE ON leases
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();
