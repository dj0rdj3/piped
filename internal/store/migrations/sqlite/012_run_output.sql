CREATE TABLE run_output (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    host_id TEXT NOT NULL,
    data BLOB NOT NULL,
    FOREIGN KEY(run_id, host_id) REFERENCES run_hosts(run_id, host_id) ON DELETE CASCADE
);
CREATE INDEX run_output_cursor ON run_output(run_id, host_id, id);
