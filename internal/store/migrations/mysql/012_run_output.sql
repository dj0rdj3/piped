CREATE TABLE run_output (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    run_id VARCHAR(32) NOT NULL,
    host_id VARCHAR(32) NOT NULL,
    data LONGBLOB NOT NULL,
    FOREIGN KEY(run_id, host_id) REFERENCES run_hosts(run_id, host_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX run_output_cursor ON run_output(run_id, host_id, id);
