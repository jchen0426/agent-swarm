-- agentswarm:min_reader 8
-- A NULL default on a nullable column is what the column gets anyway.
ALTER TABLE leases ADD COLUMN retired_at timestamptz DEFAULT NULL::timestamptz;
