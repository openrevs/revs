-- name: AddDocument :exec
INSERT INTO documents (userid, rev, hash, size, received_time, source)
VALUES (?, ?, ?, ?, ?, ?);

-- name: LatestDocumentByUser :one
SELECT * FROM documents WHERE userid = ? ORDER BY rev DESC LIMIT 1;

-- name: DocumentByUserRev :one
SELECT * FROM documents WHERE userid = ? AND rev = ?;

-- name: LatestDocuments :many
SELECT d.* FROM documents d
WHERE d.rev = (SELECT MAX(rev) FROM documents m WHERE m.userid = d.userid)
ORDER BY d.userid;

-- name: PublishedDocuments :many
SELECT d.* FROM documents d
JOIN published_head p ON p.userid = d.userid AND p.rev = d.rev
ORDER BY d.received_time DESC, d.userid;

-- name: PublishedHeads :many
SELECT * FROM published_head;

-- name: UpsertPublishedHead :exec
INSERT INTO published_head (userid, rev) VALUES (?, ?)
ON CONFLICT (userid) DO UPDATE SET rev = excluded.rev;

-- name: AddEpoch :exec
INSERT INTO epochs (epoch, kind, manifest_hash, created)
VALUES (?, ?, ?, ?);

-- name: LatestPatchEpoch :one
SELECT * FROM epochs WHERE kind = 'patch' ORDER BY epoch DESC LIMIT 1;

-- name: AllEpochs :many
SELECT * FROM epochs ORDER BY epoch, kind;
