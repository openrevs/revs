-- +goose Up
-- TV series (https://openrevs.org/specs/ratingset/#tv-series). The
-- movies table holds every item: type says whether it is a movie or a
-- series, and a viewing of a series may name a season and episode.
-- With neither, a viewing is about the item as a whole.
ALTER TABLE movies ADD COLUMN type TEXT NOT NULL DEFAULT 'movie' CHECK (type IN ('movie', 'tv'));
ALTER TABLE viewings ADD COLUMN season INTEGER CHECK (season >= 0);
ALTER TABLE viewings ADD COLUMN episode INTEGER CHECK (episode IS NULL OR (episode >= 1 AND season IS NOT NULL));

ALTER TABLE idx_titles ADD COLUMN type TEXT NOT NULL DEFAULT 'movie';
ALTER TABLE idx_reviews ADD COLUMN season INTEGER;
ALTER TABLE idx_reviews ADD COLUMN episode INTEGER;

-- +goose Down
ALTER TABLE idx_reviews DROP COLUMN episode;
ALTER TABLE idx_reviews DROP COLUMN season;
ALTER TABLE idx_titles DROP COLUMN type;
ALTER TABLE viewings DROP COLUMN episode;
ALTER TABLE viewings DROP COLUMN season;
DELETE FROM movies WHERE type != 'movie';
ALTER TABLE movies DROP COLUMN type;
