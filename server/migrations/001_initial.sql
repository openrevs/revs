-- +goose Up

-- The server's accepted-document ledger. Document blobs live in the
-- line store (see client/sqlitestore); this table holds metadata only.
-- source is where the document came from: '' for a direct upload, else
-- the URL of the upstream server it was pulled from, so an operator can
-- trace a cluster of bad documents to the server that supplied them.
CREATE TABLE documents
(
    userid text NOT NULL
    , rev integer NOT NULL
    , hash text NOT NULL
    , size integer NOT NULL
    , received_time datetime NOT NULL
    , source text NOT NULL DEFAULT ''
    , PRIMARY KEY (userid, rev)
);

-- Each channel's epochs. Epochs are per channel and consecutive.
CREATE TABLE channel_epochs
(
    channel text NOT NULL
    , epoch integer NOT NULL
    , created datetime NOT NULL
    , PRIMARY KEY (channel, epoch)
);

-- Channel membership history: from epoch on, the channel holds rev of
-- the user's document, or nothing if rev is NULL (removed). The state of
-- a channel at any epoch is each user's latest row at or before it.
CREATE TABLE channel_changes
(
    channel text NOT NULL
    , epoch integer NOT NULL
    , userid text NOT NULL
    , rev integer
    , PRIMARY KEY (channel, userid, epoch)
);

-- Published snapshots and patches. A snapshot has start = end = its
-- epoch.
CREATE TABLE artifacts
(
    channel text NOT NULL
    , kind text NOT NULL
    , start_epoch integer NOT NULL
    , end_epoch integer NOT NULL
    , parts integer NOT NULL
    , created datetime NOT NULL
    , PRIMARY KEY (channel, kind, start_epoch, end_epoch)
);

-- +goose Down
DROP TABLE artifacts;
DROP TABLE channel_changes;
DROP TABLE channel_epochs;
DROP TABLE documents;
