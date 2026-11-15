-- +goose Up

-- The server's accepted-document ledger. Document blobs live in the
-- line store; this table holds metadata only. (004 adds source, which
-- records whether the document was uploaded directly or pulled.)
CREATE TABLE documents
(
    userid text NOT NULL
    , rev integer NOT NULL
    , hash text NOT NULL
    , size integer NOT NULL
    , received_time datetime NOT NULL
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

DROP TABLE uploads;
DROP TABLE reviews;
DROP TABLE snapshots;
DROP TABLE ratingsets;

-- +goose Down
DROP TABLE documents;
DROP TABLE channel_epochs;
DROP TABLE channel_changes;
DROP TABLE artifacts;
