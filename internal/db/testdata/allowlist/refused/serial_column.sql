-- A serial column is NOT NULL with a default from a sequence: adding one
-- fills every row, a backfill.
ALTER TABLE leases ADD COLUMN takes bigserial;
