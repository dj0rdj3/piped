CREATE TABLE notification_targets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    method TEXT NOT NULL CHECK (method IN ('POST', 'PUT', 'GET')),
    body TEXT NOT NULL,
    content_type TEXT NOT NULL,
    enabled BIGINT NOT NULL CHECK (enabled IN (0, 1)),
    default_enabled BIGINT NOT NULL CHECK (default_enabled IN (0, 1)),
    version BIGINT NOT NULL CHECK (version > 0),
    target_type VARCHAR(32) NOT NULL,
    endpoint TEXT NOT NULL
);
