-- A changed type rewrites the table and changes what an older build reads.
ALTER TABLE leases ALTER COLUMN holder TYPE varchar(200);
