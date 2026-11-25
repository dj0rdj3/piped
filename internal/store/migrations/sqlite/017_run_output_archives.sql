CREATE TABLE run_output_archives (
    run_id TEXT NOT NULL,
    host_id TEXT NOT NULL,
    first_id INTEGER NOT NULL,
    last_id INTEGER NOT NULL,
    data BLOB NOT NULL,
    PRIMARY KEY(run_id, host_id, first_id),
    FOREIGN KEY(run_id, host_id) REFERENCES run_hosts(run_id, host_id) ON DELETE CASCADE,
    CHECK(first_id > 0 AND last_id >= first_id)
);
CREATE INDEX run_output_archives_cursor ON run_output_archives(run_id, host_id, last_id);
