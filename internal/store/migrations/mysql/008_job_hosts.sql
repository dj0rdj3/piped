CREATE TABLE job_hosts (
    job_id VARCHAR(32) NOT NULL,
    host_id VARCHAR(32) NOT NULL,
    position BIGINT NOT NULL CHECK (position >= 0),
    PRIMARY KEY (job_id, host_id),
    UNIQUE (job_id, position),
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (host_id) REFERENCES hosts(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX job_hosts_host_id ON job_hosts(host_id);
