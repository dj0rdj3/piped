CREATE TABLE run_admissions (
    user_id BIGINT NOT NULL,
    request_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    run_id VARCHAR(32) NOT NULL UNIQUE,
    job_id VARCHAR(32) NOT NULL,
    job_version BIGINT NOT NULL CHECK (job_version > 0),
    PRIMARY KEY (user_id, request_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
