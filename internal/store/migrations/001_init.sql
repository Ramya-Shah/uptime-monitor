-- One row per check result.
CREATE TABLE checks (
    id         BIGSERIAL PRIMARY KEY,
    site       TEXT             NOT NULL,
    checked_at TIMESTAMPTZ      NOT NULL,
    status     INT              NOT NULL, -- 0 = no response
    latency_ms DOUBLE PRECISION NOT NULL,
    ok         BOOLEAN          NOT NULL,
    error      TEXT             NOT NULL DEFAULT ''
);

-- Every query filters by site and a recent time window.
CREATE INDEX checks_site_time ON checks (site, checked_at DESC);
