CREATE TABLE encryption_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    verification BLOB NOT NULL
);
