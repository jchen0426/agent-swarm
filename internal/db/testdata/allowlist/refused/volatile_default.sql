-- A default that is not a constant fills every row anew, a backfill.
ALTER TABLE leases ADD COLUMN noted_at timestamptz DEFAULT now();
