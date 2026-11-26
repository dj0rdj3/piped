CREATE TABLE list_tags (
    kind TEXT NOT NULL,
    id TEXT NOT NULL,
    tag TEXT NOT NULL,
    PRIMARY KEY (kind, id, tag),
    FOREIGN KEY (kind, id) REFERENCES list_entries(kind, id) ON DELETE CASCADE
);
