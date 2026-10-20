-- A NULL default is no default: an older build's insert, which leaves the
-- column out, would put NULL in a NOT NULL column and fail.
ALTER TABLE leases ADD COLUMN takes integer DEFAULT NULL NOT NULL;
