CREATE TABLE runs (
    run_sequence BIGINT NOT NULL AUTO_INCREMENT UNIQUE,
    id VARCHAR(32) PRIMARY KEY,
    job_id VARCHAR(32) NOT NULL,
    job_name TEXT NOT NULL,
    script MEDIUMTEXT NOT NULL,
    execution_mode TEXT NOT NULL,
    stop_on_failure BIGINT NOT NULL,
    status TEXT NOT NULL,
    started_at BIGINT NOT NULL,
    finished_at BIGINT NOT NULL DEFAULT 0,
    run_as_sudo BIGINT NOT NULL DEFAULT 1 CHECK (run_as_sudo IN (0, 1)),
    run_source VARCHAR(16) NOT NULL DEFAULT 'manual' CHECK (run_source IN ('manual', 'schedule')),
    scheduled_at BIGINT NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX runs_job_started ON runs(job_id, started_at DESC);
CREATE INDEX runs_job_active ON runs(job_id);
