-- name: AppData :one
SELECT value FROM app_data WHERE key = ?;

-- name: SetAppData :exec
INSERT INTO app_data (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;
