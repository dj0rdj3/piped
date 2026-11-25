CREATE TABLE job_notification_targets (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL REFERENCES notification_targets(id),
    PRIMARY KEY (job_id, target_id)
);
