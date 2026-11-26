CREATE TABLE run_admissions (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_id TEXT NOT NULL,
    run_id TEXT NOT NULL UNIQUE REFERENCES runs(id) ON DELETE CASCADE,
    job_id TEXT NOT NULL,
    job_version INTEGER NOT NULL CHECK (job_version > 0),
    PRIMARY KEY (user_id, request_id)
);
