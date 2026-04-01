-- +goose Up
-- Remove legacy skill notices that were emitted as info/warning updates.
-- Pre-launch cleanup: safe to remove old path data.
DELETE FROM chat_updates
WHERE update_type IN (13, 14)
  AND (
    (data::jsonb ->> 'title') = 'Skills'
    OR lower(COALESCE(data::jsonb ->> 'message', '')) LIKE 'skill()%'
  );

-- +goose Down
-- Pre-launch migration; down migration not supported.
