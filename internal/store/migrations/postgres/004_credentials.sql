CREATE TABLE credentials (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    public_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    algorithm TEXT NOT NULL,
    has_passphrase BIGINT NOT NULL CHECK (has_passphrase IN (0, 1)),
    encrypted_secret BYTEA NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX credentials_name_unique ON credentials (translate(name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz'));
