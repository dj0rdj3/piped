-- Fold ASCII case only, matching SQLite NOCASE without folding Unicode names.
CREATE TABLE hosts (
    id VARCHAR(32) PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    address VARCHAR(253) NOT NULL,
    port BIGINT NOT NULL CHECK (port BETWEEN 1 AND 65535),
    username VARCHAR(128) NOT NULL,
    credential_id VARCHAR(32),
    encrypted_password LONGBLOB,
    run_as_sudo BIGINT NOT NULL CHECK (run_as_sudo IN (0, 1)),
    encrypted_sudo_password LONGBLOB,
    tags TEXT NOT NULL,
    version BIGINT NOT NULL CHECK (version > 0),
    created_at BIGINT NOT NULL,
    CHECK ((credential_id IS NOT NULL AND encrypted_password IS NULL)
        OR (credential_id IS NULL AND encrypted_password IS NOT NULL)),
    CHECK (run_as_sudo = 1 OR encrypted_sudo_password IS NULL),
    FOREIGN KEY (credential_id) REFERENCES credentials(id) ON DELETE RESTRICT,
    name_key VARCHAR(100) GENERATED ALWAYS AS (REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(name, 'A', 'a'), 'B', 'b'), 'C', 'c'), 'D', 'd'), 'E', 'e'), 'F', 'f'), 'G', 'g'), 'H', 'h'), 'I', 'i'), 'J', 'j'), 'K', 'k'), 'L', 'l'), 'M', 'm'), 'N', 'n'), 'O', 'o'), 'P', 'p'), 'Q', 'q'), 'R', 'r'), 'S', 's'), 'T', 't'), 'U', 'u'), 'V', 'v'), 'W', 'w'), 'X', 'x'), 'Y', 'y'), 'Z', 'z')) STORED,
    UNIQUE (name_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX hosts_credential_id ON hosts(credential_id);
