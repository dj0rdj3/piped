CREATE TABLE list_tags (
    kind VARCHAR(16) NOT NULL,
    id VARCHAR(128) NOT NULL,
    tag VARCHAR(50) NOT NULL,
    PRIMARY KEY (kind, id, tag),
    FOREIGN KEY (kind, id) REFERENCES list_entries(kind, id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
