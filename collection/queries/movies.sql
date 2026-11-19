-- name: ListMovies :many
-- Items whose title contains query (all when empty), of the given type
-- (all when empty). latest_rating is the item's rating: that of its
-- newest viewing about the whole item (not a season or episode).
SELECT m.id, m.title, m.year, m.imdb_id, m.tmdb_id, m.wikidata_id, m.type,
       CAST(COUNT(v.id) AS INTEGER) AS viewing_count,
       CAST(COALESCE(MAX(v.viewed_on), '') AS TEXT) AS last_viewed,
       -- -1 when there is no viewing or the latest one is unrated.
       CAST(COALESCE((SELECT v2.rating FROM viewings v2
             WHERE v2.movie_id = m.id AND v2.season IS NULL
             ORDER BY v2.viewed_on IS NULL, v2.viewed_on DESC, v2.id DESC
             LIMIT 1), -1) AS INTEGER) AS latest_rating
FROM movies m
LEFT JOIN viewings v ON v.movie_id = m.id
WHERE (CAST(sqlc.arg(query) AS TEXT) = '' OR m.title LIKE '%' || CAST(sqlc.arg(query) AS TEXT) || '%')
  AND (CAST(sqlc.arg(type) AS TEXT) = '' OR m.type = CAST(sqlc.arg(type) AS TEXT))
GROUP BY m.id
ORDER BY m.title COLLATE NOCASE, m.year;

-- name: AllMovies :many
SELECT * FROM movies ORDER BY title COLLATE NOCASE, year, id;

-- name: GetMovie :one
SELECT * FROM movies WHERE id = ?;

-- name: CreateMovie :one
INSERT INTO movies (type, title, year, imdb_id, tmdb_id, wikidata_id)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateMovie :exec
UPDATE movies
SET type = ?, title = ?, year = ?, imdb_id = ?, tmdb_id = ?, wikidata_id = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: DeleteMovie :exec
DELETE FROM movies WHERE id = ?;

-- name: FindMovieByWikidata :one
SELECT * FROM movies WHERE type = ? AND wikidata_id = ? LIMIT 1;

-- name: FindMovieByIMDB :one
SELECT * FROM movies WHERE type = ? AND imdb_id = ? LIMIT 1;

-- name: FindMovieByTMDB :one
SELECT * FROM movies WHERE type = ? AND tmdb_id = ? LIMIT 1;

-- name: FindMovieByTitleYear :one
SELECT * FROM movies
WHERE type = sqlc.arg(type) AND title = sqlc.arg(title) COLLATE NOCASE AND year IS sqlc.narg(year)
LIMIT 1;
