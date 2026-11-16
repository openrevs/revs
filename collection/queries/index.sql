-- name: DeleteIndexedUser :exec
DELETE FROM idx_users WHERE server_id = ? AND userid = ?;

-- name: DeleteIndexedServer :exec
DELETE FROM idx_users WHERE server_id = ?;

-- name: InsertIndexedUser :exec
INSERT INTO idx_users (server_id, userid, rev, hash, title_count) VALUES (?, ?, ?, ?, ?);

-- name: InsertIndexedTitle :one
INSERT INTO idx_titles (server_id, userid, name, year, imdb_id, tmdb_id, wikidata_id)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: InsertIndexedReview :exec
INSERT INTO idx_reviews (title_id, viewed_on, rating, comment) VALUES (?, ?, ?, ?);

-- name: CountIndexedUsers :one
SELECT COUNT(*) FROM idx_users WHERE server_id = ?;

-- name: ListIndexedUsers :many
SELECT * FROM idx_users WHERE server_id = ? ORDER BY title_count DESC, userid;

-- name: GetIndexedUser :one
SELECT * FROM idx_users WHERE server_id = ? AND userid = ?;

-- name: ListUserReviews :many
SELECT t.id AS title_id, t.name, t.year, t.imdb_id, t.tmdb_id, t.wikidata_id,
       r.viewed_on, r.rating, r.comment
FROM idx_titles t
LEFT JOIN idx_reviews r ON r.title_id = t.id
WHERE t.server_id = ? AND t.userid = ?
ORDER BY t.name COLLATE NOCASE, t.year, t.id, r.viewed_on IS NULL, r.viewed_on DESC;

-- name: MatchingReviews :many
-- Other users' reviews of a title, from every server: matched by any
-- shared external id, or by exact title (case-insensitive) and year.
-- Titles whose ids conflict (a shared id with a different value) never
-- match, per the ratingset spec.
SELECT t.server_id, t.userid, t.name, t.year, r.viewed_on, r.rating, r.comment
FROM idx_titles t
JOIN idx_reviews r ON r.title_id = t.id
WHERE t.userid != sqlc.arg(exclude_user)
  AND ((CAST(sqlc.narg(wikidata_id) AS TEXT) IS NOT NULL AND t.wikidata_id = CAST(sqlc.narg(wikidata_id) AS TEXT))
    OR (CAST(sqlc.narg(imdb_id) AS TEXT) IS NOT NULL AND t.imdb_id = CAST(sqlc.narg(imdb_id) AS TEXT))
    OR (CAST(sqlc.narg(tmdb_id) AS TEXT) IS NOT NULL AND t.tmdb_id = CAST(sqlc.narg(tmdb_id) AS TEXT))
    OR (t.name = sqlc.arg(name) COLLATE NOCASE AND t.year IS sqlc.narg(year)))
  AND (t.wikidata_id IS NULL OR CAST(sqlc.narg(wikidata_id) AS TEXT) IS NULL OR t.wikidata_id = CAST(sqlc.narg(wikidata_id) AS TEXT))
  AND (t.imdb_id IS NULL OR CAST(sqlc.narg(imdb_id) AS TEXT) IS NULL OR t.imdb_id = CAST(sqlc.narg(imdb_id) AS TEXT))
  AND (t.tmdb_id IS NULL OR CAST(sqlc.narg(tmdb_id) AS TEXT) IS NULL OR t.tmdb_id = CAST(sqlc.narg(tmdb_id) AS TEXT))
ORDER BY t.server_id, r.viewed_on IS NULL, r.viewed_on DESC;
