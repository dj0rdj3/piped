CREATE TABLE run_hosts (
    run_id VARCHAR(32) NOT NULL,
    host_id VARCHAR(32) NOT NULL,
    name VARCHAR(100) NOT NULL,
    address VARCHAR(253) NOT NULL,
    port BIGINT NOT NULL,
    position BIGINT NOT NULL,
    status TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT (''),
    exit_code BIGINT,
    started_at BIGINT NOT NULL DEFAULT 0,
    finished_at BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY(run_id, host_id),
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
