-- A column's own constraint refuses rows an older build may write, and
-- checks every row already there.
ALTER TABLE leases ADD COLUMN build integer DEFAULT 1 CHECK (build > 0);
