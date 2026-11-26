CREATE TABLE host_tags (
    host_id VARCHAR(32) NOT NULL,
    tag VARCHAR(50) NOT NULL,
    PRIMARY KEY (host_id, tag),
    FOREIGN KEY (host_id) REFERENCES hosts(id) ON DELETE CASCADE,
    INDEX host_tags_membership (tag, host_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
