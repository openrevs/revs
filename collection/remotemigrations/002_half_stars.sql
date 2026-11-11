-- +goose Up
-- Indexed ratings are now in half stars, rescaled from each document's
-- declared scale. Clear the index; remote() rebuilds an empty index
-- from the pulled documents when the database is opened.
DELETE FROM idx_users;

-- +goose Down
DELETE FROM idx_users;
