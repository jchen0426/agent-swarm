-- A child's rows show in reads of its parent, but the parent's triggers do
-- not act on them: they would be neither chained nor kept append-only.
CREATE TABLE more_tool_calls (note text) INHERITS (tool_calls);
