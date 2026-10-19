-- Leases (spec Section 8.2; ADR-004): one instance holds a lease at a time,
-- and only the holder acts. A holding has an epoch, which only grows, and
-- an expiry the holder renews. A lease's row is not an audit record: it
-- changes with every renewal.
--
-- Every write transaction of the holder takes the row in key share, as it
-- checks that its epoch still holds the lease. A renewal changes the expiry
-- only, which takes the row in no-key update: it never waits for a write. A
-- takeover changes the epoch, a column of a unique key, which takes the row
-- for update: it waits for the writes in flight, and once it commits, no
-- write of the last holding passes the check.
CREATE TABLE leases (
    tenant_id  text        NOT NULL,
    name       text        NOT NULL,
    epoch      bigint      NOT NULL CHECK (epoch > 0),
    holder     text        NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, name),
    UNIQUE (tenant_id, name, epoch)
);
