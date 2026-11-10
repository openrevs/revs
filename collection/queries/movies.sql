-- name: ListMovies :many
SELECT m.id, m.title, m.year, m.imdb_id, m.tmdb_id, m.wikidata_id,
       CAST(COUNT(v.id) AS INTEGER) AS viewing_count,
       CAST(COALESCE(MAX(v.viewed_on), '') AS TEXT) AS last_viewed,
       -- -1 when there is no viewing or the latest one is unrated.
       CAST(COALESCE((SELECT v2.rating FROM viewings v2
             WHERE v2.movie_id = m.id
             ORDER BY v2.viewed_on IS NULL, v2.viewed_on DESC, v2.id DESC
             LIMIT 1), -1) AS INTEGER) AS latest_rating
FROM movies m
LEFT JOIN viewings v ON v.movie_id = m.id
WHERE CAST(sqlc.arg(query) AS TEXT) = '' OR m.title LIKE '%' || CAST(sqlc.arg(query) AS TEXT) || '%'
GROUP BY m.id
ORDER BY m.title COLLATE NOCASE, m.year;

-- name: AllMovies :many
SELECT * FROM movies ORDER BY title COLLATE NOCASE, year, id;

-- name: GetMovie :one
SELECT * FROM movies WHERE id = ?;

-- name: CreateMovie :one
INSERT INTO movies (title, year, imdb_id, tmdb_id, wikidata_id)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateMovie :exec
UPDATE movies
SET title = ?, year = ?, imdb_id = ?, tmdb_id = ?, wikidata_id = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: DeleteMovie :exec
DELETE FROM movies WHERE id = ?;

-- name: FindMovieByWikidata :one
SELECT * FROM movies WHERE wikidata_id = ? LIMIT 1;

-- name: FindMovieByIMDB :one
SELECT * FROM movies WHERE imdb_id = ? LIMIT 1;

-- name: FindMovieByTMDB :one
SELECT * FROM movies WHERE tmdb_id = ? LIMIT 1;

-- name: FindMovieByTitleYear :one
SELECT * FROM movies
WHERE title = sqlc.arg(title) COLLATE NOCASE AND year IS sqlc.narg(year)
LIMIT 1;
