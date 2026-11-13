-- name: AddDocument :exec
INSERT INTO documents (userid, rev, hash, size, received_time, direct)
VALUES (?, ?, ?, ?, ?, ?);

-- name: LatestDocumentByUser :one
SELECT * FROM documents WHERE userid = ? ORDER BY rev DESC LIMIT 1;

-- name: DocumentByUserRev :one
SELECT * FROM documents WHERE userid = ? AND rev = ?;

-- name: LatestDocuments :many
SELECT d.* FROM documents d
WHERE d.rev = (SELECT MAX(rev) FROM documents m WHERE m.userid = d.userid)
ORDER BY d.userid;

-- name: LatestDirectDocuments :many
SELECT d.* FROM documents d
WHERE d.direct
  AND d.rev = (SELECT MAX(rev) FROM documents m WHERE m.userid = d.userid AND m.direct)
ORDER BY d.userid;

-- name: ChannelDocuments :many
-- The documents a channel holds at an epoch, newest-first.
SELECT d.* FROM channel_changes c
JOIN documents d ON d.userid = c.userid AND d.rev = c.rev
WHERE c.channel = sqlc.arg(channel)
  AND c.epoch = (
    SELECT MAX(m.epoch) FROM channel_changes m
    WHERE m.channel = c.channel AND m.userid = c.userid AND m.epoch <= sqlc.arg(epoch)
  )
ORDER BY d.received_time DESC, d.userid;

-- name: AddChannelChange :exec
INSERT INTO channel_changes (channel, epoch, userid, rev) VALUES (?, ?, ?, ?);

-- name: LatestChannelEpoch :one
SELECT CAST(COALESCE(MAX(epoch), 0) AS integer) FROM channel_epochs WHERE channel = ?;

-- name: AddChannelEpoch :exec
INSERT INTO channel_epochs (channel, epoch, created) VALUES (?, ?, ?);

-- name: AddArtifact :exec
INSERT INTO artifacts (channel, kind, start_epoch, end_epoch, parts, created)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ChannelArtifacts :many
SELECT * FROM artifacts WHERE channel = ?
ORDER BY kind, start_epoch, end_epoch;
