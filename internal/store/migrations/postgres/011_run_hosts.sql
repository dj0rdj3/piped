CREATE TABLE run_hosts (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    host_id TEXT NOT NULL,
    name TEXT NOT NULL,
    address TEXT NOT NULL,
    port BIGINT NOT NULL,
    position BIGINT NOT NULL,
    status TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    exit_code BIGINT,
    started_at BIGINT NOT NULL DEFAULT 0,
    finished_at BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY(run_id, host_id)
);
