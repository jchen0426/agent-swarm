-- A unique index refuses rows an older build may write.
CREATE UNIQUE INDEX leases_by_holder ON leases (holder);
