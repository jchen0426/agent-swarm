-- A NULL default is no default, cast or not.
ALTER TABLE leases ADD COLUMN takes integer NOT NULL DEFAULT NULL::integer;
