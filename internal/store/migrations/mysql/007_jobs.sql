-- Fold ASCII case only, matching SQLite NOCASE without folding Unicode names.
CREATE TABLE jobs (
    id VARCHAR(32) PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    script MEDIUMTEXT NOT NULL,
    execution_mode TEXT NOT NULL CHECK (execution_mode IN ('sequential', 'parallel')),
    stop_on_failure BIGINT NOT NULL CHECK (stop_on_failure IN (0, 1)),
    version BIGINT NOT NULL CHECK (version > 0),
    created_at BIGINT NOT NULL,
    run_as_sudo BIGINT NOT NULL DEFAULT 0 CHECK (run_as_sudo IN (0, 1)),
    schedule_cron VARCHAR(256) NOT NULL DEFAULT '',
    schedule_enabled BIGINT NOT NULL DEFAULT 0,
    schedule_after BIGINT NOT NULL DEFAULT 0,
    last_schedule_minute BIGINT NOT NULL DEFAULT 0,
    CHECK (schedule_enabled IN (0, 1) AND (schedule_enabled = 0 OR schedule_cron <> '')),
    name_key VARCHAR(100) GENERATED ALWAYS AS (REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(name, 'A', 'a'), 'B', 'b'), 'C', 'c'), 'D', 'd'), 'E', 'e'), 'F', 'f'), 'G', 'g'), 'H', 'h'), 'I', 'i'), 'J', 'j'), 'K', 'k'), 'L', 'l'), 'M', 'm'), 'N', 'n'), 'O', 'o'), 'P', 'p'), 'Q', 'q'), 'R', 'r'), 'S', 's'), 'T', 't'), 'U', 'u'), 'V', 'v'), 'W', 'w'), 'X', 'x'), 'Y', 'y'), 'Z', 'z')) STORED,
    UNIQUE (name_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX jobs_schedule_enabled ON jobs(schedule_enabled);
