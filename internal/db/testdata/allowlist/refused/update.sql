-- A backfill changes rows an older build reads.
UPDATE leases SET holder = lower(holder) WHERE holder <> lower(holder);
