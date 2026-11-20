-- +goose Up
-- What a server's index last said about it (client.ServerInfo as
-- JSON): fetched when the server is added, refreshed on every pull.
ALTER TABLE servers ADD COLUMN info TEXT;

-- +goose Down
ALTER TABLE servers DROP COLUMN info;
