CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL,
    job_name TEXT NOT NULL,
    script TEXT NOT NULL,
    execution_mode TEXT NOT NULL,
    stop_on_failure INTEGER NOT NULL,
    status TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER NOT NULL DEFAULT 0,
    run_as_sudo INTEGER NOT NULL DEFAULT 1 CHECK (run_as_sudo IN (0, 1)),
    trigger TEXT NOT NULL DEFAULT 'manual' CHECK (trigger IN ('manual', 'schedule')),
    scheduled_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX runs_job_started ON runs(job_id, started_at DESC);
CREATE INDEX runs_job_active ON runs(job_id) WHERE finished_at = 0;
