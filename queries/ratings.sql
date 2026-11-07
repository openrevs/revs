-- name: UserRatings :many
SELECT r.rating_id, r.user_id, r.rating, r.comment, r.date, r.updated,
       t.title_id, t.type, t.name, t.year, t.tmdb_id, t.tmdb_poster_url
FROM ratings r
JOIN titles t ON r.title_id = t.title_id
WHERE r.user_id = ?;

-- name: TitleRatings :many
SELECT r.rating_id, r.user_id, r.rating, r.comment, r.date, r.updated,
       t.title_id, t.type, t.name, t.year, t.tmdb_id, t.tmdb_poster_url
FROM ratings r
JOIN titles t ON r.title_id = t.title_id
WHERE r.title_id = ?;

-- name: Rating :one
SELECT r.rating_id, r.user_id, r.rating, r.comment, r.date, r.updated,
       t.title_id, t.type, t.name, t.year, t.tmdb_id, t.tmdb_poster_url
FROM ratings r
JOIN titles t ON r.title_id = t.title_id
WHERE r.rating_id = ?;

-- name: InsertRating :exec
INSERT INTO ratings (title_id, user_id, rating, comment, updated)
VALUES (?, ?, ?, ?, ?);

-- name: UpdateRating :exec
UPDATE ratings
SET title_id = ?, user_id = ?, rating = ?, comment = ?, updated = ?
WHERE rating_id = ?;

-- name: DeleteRating :exec
DELETE FROM ratings WHERE rating_id = ?;

-- name: TitleRatingCount :one
SELECT COUNT(*) FROM ratings WHERE title_id = ?;

-- name: TitleRatingAvg :one
SELECT AVG(rating) FROM ratings WHERE title_id = ? AND rating IS NOT NULL;
