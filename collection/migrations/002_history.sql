-- +goose Up
-- The author's version history (client.History): every version of the
-- local collection signed for pushing or export, shared by all servers.
CREATE TABLE history (
    userid      TEXT PRIMARY KEY,
    foreign_rev INTEGER NOT NULL
);

CREATE TABLE history_versions (
    userid TEXT NOT NULL REFERENCES history (userid) ON DELETE CASCADE,
    seq    INTEGER NOT NULL,
    rev    INTEGER NOT NULL,
    hash   TEXT NOT NULL,
    data   BLOB NOT NULL,
    PRIMARY KEY (userid, seq)
);

-- +goose Down
DROP TABLE history_versions;
DROP TABLE history;
