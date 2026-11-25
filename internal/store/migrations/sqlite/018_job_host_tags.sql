CREATE TABLE job_host_tags (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    tag TEXT NOT NULL,
    PRIMARY KEY (job_id, tag)
);
