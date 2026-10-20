-- agentswarm:min_reader 8
-- A nullable column on a table no trigger names: an older build reads the
-- table as it did, and its inserts leave the column null.
ALTER TABLE leases ADD COLUMN note text;
