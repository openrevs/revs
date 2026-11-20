-- name: ListViewingsForMovie :many
SELECT * FROM viewings
WHERE movie_id = ?
ORDER BY viewed_on IS NULL, viewed_on DESC, created_at DESC, id DESC;

-- name: AllViewings :many
SELECT * FROM viewings ORDER BY movie_id, viewed_on, id;

-- name: History :many
-- Newest viewing date first; undated viewings last, most recently
-- added first.
SELECT v.id, v.movie_id, v.viewed_on, v.rating, v.review, v.season, v.episode, v.created_at, m.title, m.year, m.type
FROM viewings v
JOIN movies m ON m.id = v.movie_id
ORDER BY v.viewed_on IS NULL, v.viewed_on DESC, v.created_at DESC, v.id DESC
LIMIT ?;

-- name: HistoryByAdded :many
-- Most recently added first, whatever their viewing dates.
SELECT v.id, v.movie_id, v.viewed_on, v.rating, v.review, v.season, v.episode, v.created_at, m.title, m.year, m.type
FROM viewings v
JOIN movies m ON m.id = v.movie_id
ORDER BY v.created_at DESC, v.id DESC
LIMIT ?;

-- name: GetViewing :one
SELECT * FROM viewings WHERE id = ?;

-- name: CreateViewing :one
INSERT INTO viewings (movie_id, viewed_on, rating, review, season, episode)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateViewing :exec
UPDATE viewings
SET viewed_on = ?, rating = ?, review = ?, season = ?, episode = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: DeleteViewing :exec
DELETE FROM viewings WHERE id = ?;

-- name: CountMoviesAndViewings :one
SELECT (SELECT COUNT(*) FROM movies WHERE type = 'movie') AS movies,
       (SELECT COUNT(*) FROM movies WHERE type = 'tv') AS series,
       (SELECT COUNT(*) FROM viewings) AS viewings;

-- name: CountSeasonViewings :one
SELECT COUNT(*) FROM viewings WHERE movie_id = ? AND season IS NOT NULL;
