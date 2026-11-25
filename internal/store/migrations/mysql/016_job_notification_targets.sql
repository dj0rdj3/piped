CREATE TABLE job_notification_targets (
    job_id VARCHAR(32) NOT NULL,
    target_id VARCHAR(32) NOT NULL,
    PRIMARY KEY (job_id, target_id),
    FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE,
    FOREIGN KEY (target_id) REFERENCES notification_targets(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
