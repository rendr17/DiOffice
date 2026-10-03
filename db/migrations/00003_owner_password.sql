-- +goose Up
ALTER TABLE users
    ADD COLUMN password_hash text CHECK (password_hash IS NULL OR length(password_hash) = 60);

-- +goose Down
ALTER TABLE users DROP COLUMN password_hash;
