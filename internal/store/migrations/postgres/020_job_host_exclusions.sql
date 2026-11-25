CREATE TABLE job_host_exclusions (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    PRIMARY KEY (job_id, host_id)
);
CREATE INDEX job_host_exclusions_host_id ON job_host_exclusions(host_id);
