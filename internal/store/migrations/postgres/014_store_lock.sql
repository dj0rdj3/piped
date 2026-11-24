CREATE TABLE store_lock (
    id BIGINT PRIMARY KEY CHECK (id = 1)
);
INSERT INTO store_lock(id) VALUES (1);
