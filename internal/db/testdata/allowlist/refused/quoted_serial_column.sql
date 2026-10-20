-- A serial type quoted is still a serial: Postgres reads the name without its
-- quotes.
ALTER TABLE leases ADD COLUMN takes "bigserial";
