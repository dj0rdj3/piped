CREATE TABLE execution_ownership (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    token VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
) ENGINE=InnoDB;
INSERT INTO execution_ownership (id, token) VALUES (1, '');
