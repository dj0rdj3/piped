CREATE TABLE list_entries (
    kind VARCHAR(16) NOT NULL,
    id VARCHAR(128) NOT NULL,
    owner_id BIGINT NOT NULL,
    name_key VARBINARY(1024) NOT NULL,
    search_text LONGTEXT NOT NULL,
    host_search LONGTEXT NOT NULL,
    PRIMARY KEY (kind, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX list_entries_order ON list_entries(kind, owner_id, name_key, id);
