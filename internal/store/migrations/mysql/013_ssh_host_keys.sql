CREATE TABLE ssh_host_keys (
    host_id VARCHAR(32) NOT NULL,
    address VARCHAR(253) NOT NULL,
    port BIGINT NOT NULL,
    public_key TEXT NOT NULL,
    PRIMARY KEY(host_id, address, port),
    FOREIGN KEY (host_id) REFERENCES hosts(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
