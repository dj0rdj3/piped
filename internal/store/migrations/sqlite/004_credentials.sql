CREATE TABLE credentials (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    public_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    algorithm TEXT NOT NULL,
    has_passphrase INTEGER NOT NULL CHECK (has_passphrase IN (0, 1)),
    encrypted_secret BLOB NOT NULL,
    created_at INTEGER NOT NULL
);
