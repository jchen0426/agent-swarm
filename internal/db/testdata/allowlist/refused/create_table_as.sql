-- A table made from a query copies rows, and the query may call functions
-- that write, as audit_chain_append does.
CREATE TABLE call_copies AS SELECT * FROM tool_calls;
