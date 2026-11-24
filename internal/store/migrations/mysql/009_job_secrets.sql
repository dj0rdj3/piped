CREATE TABLE job_secrets (
    job_id VARCHAR(32) NOT NULL,
    name VARCHAR(100) NOT NULL,
    encrypted_value LONGBLOB NOT NULL,
    PRIMARY KEY (job_id, name),
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
