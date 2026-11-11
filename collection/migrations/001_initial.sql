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

-- A searchable index of the documents pulled from each server. The
-- verified documents themselves are in sqlitestore's corpus_ tables
-- (one feed per server); these rows are derived from them and can be
-- rebuilt.
CREATE TABLE idx_users (
    server_id   INTEGER NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    userid      TEXT NOT NULL,
    rev         INTEGER NOT NULL,
    hash        TEXT NOT NULL,
    title_count INTEGER NOT NULL,
    PRIMARY KEY (server_id, userid)
);

CREATE TABLE idx_titles (
    id          INTEGER PRIMARY KEY,
    server_id   INTEGER NOT NULL,
    userid      TEXT NOT NULL,
    name        TEXT NOT NULL,
    year        INTEGER,
    imdb_id     TEXT,
    tmdb_id     TEXT,
    wikidata_id TEXT,
    FOREIGN KEY (server_id, userid) REFERENCES idx_users (server_id, userid) ON DELETE CASCADE
);
CREATE INDEX idx_titles_user ON idx_titles (server_id, userid);
CREATE INDEX idx_titles_name ON idx_titles (name COLLATE NOCASE);
CREATE INDEX idx_titles_imdb ON idx_titles (imdb_id);
CREATE INDEX idx_titles_tmdb ON idx_titles (tmdb_id);
CREATE INDEX idx_titles_wikidata ON idx_titles (wikidata_id);

CREATE TABLE idx_reviews (
    id        INTEGER PRIMARY KEY,
    title_id  INTEGER NOT NULL REFERENCES idx_titles (id) ON DELETE CASCADE,
    viewed_on TEXT,
    rating    INTEGER,
    comment   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_reviews_title ON idx_reviews (title_id);

-- +goose Down
DROP TABLE idx_reviews;
DROP TABLE idx_titles;
DROP TABLE idx_users;
DROP TABLE servers;
DROP TABLE settings;
DROP TABLE viewings;
DROP TABLE movies;
