CREATE TABLE list_entries (
    kind TEXT NOT NULL,
    id TEXT NOT NULL,
    owner_id BIGINT NOT NULL,
    name_key BYTEA NOT NULL,
    search_text TEXT NOT NULL,
    host_search TEXT NOT NULL,
    PRIMARY KEY (kind, id)
);
CREATE INDEX list_entries_order ON list_entries(kind, owner_id, name_key, id);
