-- A constraint refuses rows an older build may write.
ALTER TABLE leases ADD CONSTRAINT leases_holder_named CHECK (holder <> '');
