-- A name spelled with Unicode escapes is the name it spells: this type is
-- bigserial, a serial.
ALTER TABLE leases ADD COLUMN takes U&"\0062igserial";
