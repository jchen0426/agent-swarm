-- A column on an audited table: the table's chain form lists its columns, so
-- the new column would go unchained.
ALTER TABLE tool_call_outcomes ADD COLUMN failure text;
