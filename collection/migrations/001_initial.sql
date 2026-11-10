-- +goose Up
CREATE TABLE movies (
    id          INTEGER PRIMARY KEY,
    title       TEXT NOT NULL,
    year        INTEGER,
    imdb_id     TEXT,
    tmdb_id     TEXT,
    wikidata_id TEXT,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX movies_title ON movies (title COLLATE NOCASE);
CREATE INDEX movies_wikidata ON movies (wikidata_id);

CREATE TABLE viewings (
    id         INTEGER PRIMARY KEY,
    movie_id   INTEGER NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    viewed_on  TEXT,
    rating     INTEGER CHECK (rating BETWEEN 0 AND 5),
    review     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX viewings_movie ON viewings (movie_id);
CREATE INDEX viewings_viewed_on ON viewings (viewed_on);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE servers (
    id            INTEGER PRIMARY KEY,
    url           TEXT NOT NULL UNIQUE,
    last_push_at  TEXT,
    last_push_rev INTEGER,
    last_pull_at  TEXT,
    last_epoch    INTEGER,
    last_error    TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- +goose Down
DROP TABLE servers;
DROP TABLE settings;
DROP TABLE viewings;
DROP TABLE movies;
