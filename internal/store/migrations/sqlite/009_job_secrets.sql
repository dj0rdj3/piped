CREATE TABLE job_secrets (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    encrypted_value BLOB NOT NULL,
    PRIMARY KEY (job_id, name)
);
