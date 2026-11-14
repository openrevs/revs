-- +goose Up

-- Where each accepted document came from: '' for a direct upload, else
-- the URL of the upstream server it was pulled from. Knowing a
-- document's origin lets an operator trace a cluster of bad documents
-- to the server that supplied them.
ALTER TABLE documents ADD COLUMN source text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE documents DROP COLUMN source;
