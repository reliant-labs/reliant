-- +goose Up
-- Fix workflow_id format to include thread path suffix (-0)
-- This ensures chat.workflow_id matches the actual Temporal workflow ID
-- Without this fix, GetChatState cannot query workflow status and loading indicators don't work

UPDATE chats
SET workflow_id = workflow_id || '-0'
WHERE workflow_id NOT LIKE '%-0'
  AND workflow_id IS NOT NULL
  AND workflow_id != '';

-- +goose Down
-- Remove the -0 suffix (reverting to broken state, but included for completeness)
UPDATE chats
SET workflow_id = REPLACE(workflow_id, '-0', '')
WHERE workflow_id LIKE '%-0';
