CREATE TABLE ssh_host_keys (
    host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    address TEXT NOT NULL,
    port INTEGER NOT NULL,
    public_key TEXT NOT NULL,
    PRIMARY KEY(host_id, address, port)
);
