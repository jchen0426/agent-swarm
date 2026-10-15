-- A write call ends as recorded: the gateway recorded it as a proposal for a
-- person and sent nothing (spec Section 6.3). It is not a read, so evidence
-- cannot cite it.
ALTER TABLE tool_call_outcomes DROP CONSTRAINT tool_call_outcomes_status_check;
ALTER TABLE tool_call_outcomes ADD CONSTRAINT tool_call_outcomes_status_check
    CHECK (status IN ('ok', 'read_error', 'refused', 'recorded'));

-- A platform read made for an agent's write call, for its candidates or its
-- facts, names that call, so that it is never taken for one of the
-- platform's own reads.
ALTER TABLE tool_calls ADD COLUMN cause text NOT NULL DEFAULT '';
