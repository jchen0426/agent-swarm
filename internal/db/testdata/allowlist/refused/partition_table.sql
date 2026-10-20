-- A partition's rows are its parent's.
CREATE TABLE work_records_2027 PARTITION OF work_records FOR VALUES FROM ('2027-01-01') TO ('2028-01-01');
