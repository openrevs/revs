-- name: Titles :many
SELECT * FROM titles WHERE name LIKE ?;

-- name: Title :one
SELECT * FROM titles WHERE title_id = ?;

-- name: AddTitle :execlastid
INSERT INTO titles (type, name, year, tmdb_id, tmdb_poster_url)
VALUES (?, ?, ?, ?, ?);
