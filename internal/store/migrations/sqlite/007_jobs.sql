CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    script TEXT NOT NULL,
    execution_mode TEXT NOT NULL CHECK (execution_mode IN ('sequential', 'parallel')),
    stop_on_failure INTEGER NOT NULL CHECK (stop_on_failure IN (0, 1)),
    version INTEGER NOT NULL CHECK (version > 0),
    created_at INTEGER NOT NULL,
    run_as_sudo INTEGER NOT NULL DEFAULT 0 CHECK (run_as_sudo IN (0, 1)),
    schedule_cron TEXT NOT NULL DEFAULT '',
    schedule_enabled INTEGER NOT NULL DEFAULT 0 CHECK (schedule_enabled IN (0, 1) AND (schedule_enabled = 0 OR schedule_cron <> '')),
    schedule_after INTEGER NOT NULL DEFAULT 0,
    last_schedule_minute INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX jobs_schedule_enabled ON jobs(schedule_enabled) WHERE schedule_enabled = 1;
