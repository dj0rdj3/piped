CREATE TABLE notification_secrets (
    target_id TEXT NOT NULL,
    name TEXT NOT NULL,
    encrypted_value BYTEA NOT NULL,
    PRIMARY KEY (target_id, name),
    FOREIGN KEY (target_id) REFERENCES notification_targets(id) ON DELETE CASCADE
);
