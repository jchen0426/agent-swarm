-- agentswarm:min_reader 8
-- Columns with constant defaults, NOT NULL or not: an older build's inserts
-- take the defaults.
ALTER TABLE leases
    ADD COLUMN build integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS labels jsonb DEFAULT '{}'::jsonb,
    ADD region text DEFAULT 'eu-1' NOT NULL;
