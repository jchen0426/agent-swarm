-- A changed default changes what an older build's inserts write.
ALTER TABLE leases ALTER COLUMN holder SET DEFAULT 'nobody';
