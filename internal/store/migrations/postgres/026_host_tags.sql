CREATE TABLE host_tags (
    host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    tag TEXT NOT NULL,
    PRIMARY KEY (host_id, tag)
);
CREATE INDEX host_tags_membership ON host_tags(tag, host_id);
