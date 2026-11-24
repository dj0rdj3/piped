CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    script TEXT NOT NULL,
    execution_mode TEXT NOT NULL CHECK (execution_mode IN ('sequential', 'parallel')),
    stop_on_failure BIGINT NOT NULL CHECK (stop_on_failure IN (0, 1)),
    version BIGINT NOT NULL CHECK (version > 0),
    created_at BIGINT NOT NULL,
    run_as_sudo BIGINT NOT NULL DEFAULT 0 CHECK (run_as_sudo IN (0, 1)),
    schedule_cron TEXT NOT NULL DEFAULT '',
    schedule_enabled BIGINT NOT NULL DEFAULT 0 CHECK (schedule_enabled IN (0, 1) AND (schedule_enabled = 0 OR schedule_cron <> '')),
    schedule_after BIGINT NOT NULL DEFAULT 0,
    last_schedule_minute BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX jobs_schedule_enabled ON jobs(schedule_enabled) WHERE schedule_enabled = 1;
CREATE UNIQUE INDEX jobs_name_unique ON jobs (translate(name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz'));
