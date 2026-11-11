-- +goose Up
-- Ratings move from whole stars (0–5) to half stars (0–10). SQLite
-- can't alter a CHECK constraint, so the table is rebuilt; nothing
-- references viewings.
CREATE TABLE viewings_new (
    id         INTEGER PRIMARY KEY,
    movie_id   INTEGER NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    viewed_on  TEXT,
    rating     INTEGER CHECK (rating BETWEEN 0 AND 10),
    review     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
INSERT INTO viewings_new (id, movie_id, viewed_on, rating, review, created_at, updated_at)
    SELECT id, movie_id, viewed_on, rating * 2, review, created_at, updated_at FROM viewings;
DROP TABLE viewings;
ALTER TABLE viewings_new RENAME TO viewings;
CREATE INDEX viewings_movie ON viewings (movie_id);
CREATE INDEX viewings_viewed_on ON viewings (viewed_on);

-- +goose Down
CREATE TABLE viewings_old (
    id         INTEGER PRIMARY KEY,
    movie_id   INTEGER NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    viewed_on  TEXT,
    rating     INTEGER CHECK (rating BETWEEN 0 AND 5),
    review     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
INSERT INTO viewings_old (id, movie_id, viewed_on, rating, review, created_at, updated_at)
    SELECT id, movie_id, viewed_on, rating / 2, review, created_at, updated_at FROM viewings;
DROP TABLE viewings;
ALTER TABLE viewings_old RENAME TO viewings;
CREATE INDEX viewings_movie ON viewings (movie_id);
CREATE INDEX viewings_viewed_on ON viewings (viewed_on);
