CREATE TABLE notification_secrets (
    target_id VARCHAR(32) NOT NULL,
    name VARCHAR(64) NOT NULL,
    encrypted_value MEDIUMBLOB NOT NULL,
    PRIMARY KEY (target_id, name),
    FOREIGN KEY (target_id) REFERENCES notification_targets(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
