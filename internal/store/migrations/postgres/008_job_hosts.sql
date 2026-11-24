CREATE TABLE job_hosts (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE RESTRICT,
    position BIGINT NOT NULL CHECK (position >= 0),
    PRIMARY KEY (job_id, host_id),
    UNIQUE (job_id, position)
);
CREATE INDEX job_hosts_host_id ON job_hosts(host_id);
