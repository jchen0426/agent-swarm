-- A trigger on a table spelled with Unicode escapes would hide that table
-- from the audited ones, and with it every column added to it later.
CREATE TRIGGER leases_no_delete
    BEFORE DELETE ON U&"\006Ceases"
    FOR EACH ROW EXECUTE FUNCTION audit_records_guard();
