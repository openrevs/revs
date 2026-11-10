-- name: ListViewingsForMovie :many
SELECT * FROM viewings
WHERE movie_id = ?
ORDER BY viewed_on IS NULL, viewed_on DESC, id DESC;

-- name: AllViewings :many
SELECT * FROM viewings ORDER BY movie_id, viewed_on, id;

-- name: History :many
SELECT v.id, v.movie_id, v.viewed_on, v.rating, v.review, m.title, m.year
FROM viewings v
JOIN movies m ON m.id = v.movie_id
ORDER BY v.viewed_on IS NULL, v.viewed_on DESC, v.id DESC
LIMIT ?;

-- name: GetViewing :one
SELECT * FROM viewings WHERE id = ?;

-- name: CreateViewing :one
INSERT INTO viewings (movie_id, viewed_on, rating, review)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: UpdateViewing :exec
UPDATE viewings
SET viewed_on = ?, rating = ?, review = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: DeleteViewing :exec
DELETE FROM viewings WHERE id = ?;

-- name: CountMoviesAndViewings :one
SELECT (SELECT COUNT(*) FROM movies) AS movies,
       (SELECT COUNT(*) FROM viewings) AS viewings;
