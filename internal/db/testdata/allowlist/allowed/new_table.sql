-- agentswarm:min_reader 8
-- A table no trigger names, with the constraints of its own: no older build
-- writes it.
CREATE TABLE notes (
    tenant_id text        NOT NULL,
    id        text        NOT NULL,
    body      text        NOT NULL DEFAULT '',
    noted_at  timestamptz NOT NULL DEFAULT now() CHECK (noted_at > '2026-01-01'),
    PRIMARY KEY (tenant_id, id)
);
