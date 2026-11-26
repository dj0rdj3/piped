CREATE TABLE execution_ownership (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token TEXT NOT NULL
);
INSERT INTO execution_ownership (id, token) VALUES (1, '');
