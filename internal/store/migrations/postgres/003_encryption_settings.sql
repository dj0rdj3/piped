CREATE TABLE encryption_settings (
    id BIGINT PRIMARY KEY CHECK (id = 1),
    verification BYTEA NOT NULL
);
