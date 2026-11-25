CREATE TABLE api_keys (
    id VARCHAR(32) PRIMARY KEY,
    user_id BIGINT NOT NULL,
    name VARCHAR(100) NOT NULL,
    token_hash VARCHAR(64) NOT NULL UNIQUE,
    scopes TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT,
    purpose VARCHAR(16) NOT NULL DEFAULT 'development' CHECK (purpose IN ('development', 'production')),
    CONSTRAINT api_keys_development_expiry CHECK (expires_at IS NOT NULL OR purpose = 'production'),
    CHECK (expires_at > created_at),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE INDEX api_keys_user_id ON api_keys(user_id);
