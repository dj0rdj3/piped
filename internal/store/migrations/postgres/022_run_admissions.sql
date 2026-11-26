CREATE TABLE run_admissions (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_id VARCHAR(128) COLLATE "C" NOT NULL,
    run_id TEXT NOT NULL UNIQUE REFERENCES runs(id) ON DELETE CASCADE,
    job_id TEXT NOT NULL,
    job_version BIGINT NOT NULL CHECK (job_version > 0),
    PRIMARY KEY (user_id, request_id)
);
