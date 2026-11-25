CREATE TABLE job_host_exclusions (
    job_id VARCHAR(32) NOT NULL,
    host_id VARCHAR(32) NOT NULL,
    PRIMARY KEY (job_id, host_id),
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (host_id) REFERENCES hosts(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX job_host_exclusions_host_id ON job_host_exclusions(host_id);
