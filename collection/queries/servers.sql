-- name: ListServers :many
SELECT * FROM servers ORDER BY id;

-- name: GetServer :one
SELECT * FROM servers WHERE id = ?;

-- name: CreateServer :one
INSERT INTO servers (url, info) VALUES (?, ?) RETURNING *;

-- name: DeleteServer :exec
DELETE FROM servers WHERE id = ?;

-- name: RecordPush :exec
UPDATE servers
SET last_push_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), last_push_rev = ?, last_error = NULL
WHERE id = ?;

-- name: RecordPull :exec
UPDATE servers
SET last_pull_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), last_epoch = ?, info = ?, last_error = NULL
WHERE id = ?;

-- name: RecordServerError :exec
UPDATE servers SET last_error = ? WHERE id = ?;
