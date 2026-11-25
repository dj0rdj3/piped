CREATE TABLE api_keys (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    scopes TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT,
    purpose VARCHAR(16) NOT NULL DEFAULT 'development' CHECK (purpose IN ('development', 'production')),
    CONSTRAINT api_keys_development_expiry CHECK (expires_at IS NOT NULL OR purpose = 'production'),
    CHECK (expires_at > created_at)
);
CREATE INDEX api_keys_user_id ON api_keys(user_id);
