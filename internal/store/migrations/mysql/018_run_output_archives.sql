CREATE TABLE run_output_archives (
    run_id VARCHAR(32) NOT NULL,
    host_id VARCHAR(32) NOT NULL,
    first_id BIGINT NOT NULL,
    last_id BIGINT NOT NULL,
    data LONGBLOB NOT NULL,
    PRIMARY KEY(run_id, host_id, first_id),
    FOREIGN KEY(run_id, host_id) REFERENCES run_hosts(run_id, host_id) ON DELETE CASCADE,
    CHECK(first_id > 0 AND last_id >= first_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX run_output_archives_cursor ON run_output_archives(run_id, host_id, last_id);
