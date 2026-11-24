CREATE TABLE hosts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    address TEXT NOT NULL,
    port BIGINT NOT NULL CHECK (port BETWEEN 1 AND 65535),
    username TEXT NOT NULL,
    credential_id TEXT REFERENCES credentials(id) ON DELETE RESTRICT,
    encrypted_password BYTEA,
    run_as_sudo BIGINT NOT NULL CHECK (run_as_sudo IN (0, 1)),
    encrypted_sudo_password BYTEA,
    tags TEXT NOT NULL,
    version BIGINT NOT NULL CHECK (version > 0),
    created_at BIGINT NOT NULL,
    CHECK ((credential_id IS NOT NULL AND encrypted_password IS NULL)
        OR (credential_id IS NULL AND encrypted_password IS NOT NULL)),
    CHECK (run_as_sudo = 1 OR encrypted_sudo_password IS NULL)
);
CREATE INDEX hosts_credential_id ON hosts(credential_id);
CREATE UNIQUE INDEX hosts_name_unique ON hosts (translate(name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz'));
