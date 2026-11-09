-- +goose Up
-- A searchable index of the documents pulled from one server. The
-- verified documents themselves live in the client store's tables in
-- this same file; these rows are derived from them and can be rebuilt.
CREATE TABLE idx_users (
    userid      TEXT PRIMARY KEY,
    rev         INTEGER NOT NULL,
    hash        TEXT NOT NULL,
    title_count INTEGER NOT NULL
);

CREATE TABLE idx_titles (
    id          INTEGER PRIMARY KEY,
    userid      TEXT NOT NULL REFERENCES idx_users (userid) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    year        INTEGER,
    imdb_id     TEXT,
    tmdb_id     TEXT,
    wikidata_id TEXT
);
CREATE INDEX idx_titles_user ON idx_titles (userid);
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
