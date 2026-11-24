CREATE TABLE encryption_settings (
    id BIGINT PRIMARY KEY CHECK (id = 1),
    verification LONGBLOB NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
