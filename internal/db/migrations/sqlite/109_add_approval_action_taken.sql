-- +goose Up
-- Add action_taken column to store which action button was clicked when resolving an approval
-- This enables CEL expressions to route based on the specific action taken (e.g., "Deploy Now" vs "Cancel")

ALTER TABLE approvals ADD COLUMN action_taken TEXT;

-- +goose Down
-- SQLite doesn't support DROP COLUMN directly, but since action_taken is nullable 
-- and the column will just be ignored if not used, we can leave it in place for down migration
-- If a clean removal is needed, the table would need to be recreated

-- For a proper down migration, we'd need to recreate the table:
-- But since this is a nullable column that doesn't break anything, we'll skip the complex recreation
