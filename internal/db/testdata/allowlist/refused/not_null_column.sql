-- A NOT NULL column without a default refuses an older build's inserts,
-- which do not name it.
ALTER TABLE leases ADD COLUMN build integer NOT NULL;
