-- +goose Up

-- The server's accepted-document ledger. Documents themselves are
-- content-addressed blobs on disk; this table holds metadata only.
CREATE TABLE documents
(
    userid text NOT NULL
    , rev integer NOT NULL
    , hash text NOT NULL
    , size integer NOT NULL
    , received_time datetime NOT NULL
    , PRIMARY KEY (userid, rev)
);

-- The cut ledger. Patch cuts advance the epoch counter; snapshots
-- materialize the corpus at an existing epoch (same number, kind
-- 'snapshot').
CREATE TABLE epochs
(
    epoch integer NOT NULL
    , kind text NOT NULL
    , manifest_hash text NOT NULL
    , created datetime NOT NULL
    , PRIMARY KEY (epoch, kind)
);

-- What the last cut shipped per user; diffs run published rev -> latest
-- accepted rev.
CREATE TABLE published_head
(
    userid text PRIMARY KEY NOT NULL
    , rev integer NOT NULL
);

DROP TABLE uploads;
DROP TABLE reviews;
DROP TABLE snapshots;
DROP TABLE ratingsets;

-- +goose Down
DROP TABLE documents;
DROP TABLE epochs;
DROP TABLE published_head;
