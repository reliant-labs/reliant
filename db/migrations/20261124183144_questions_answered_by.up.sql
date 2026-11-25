-- forge:backward-compatible — a nullable column the previous release never reads or writes; NULL means the chat's owner answered, or nobody yet, which is what every existing row is.

-- Migration: questions_answered_by
--
-- The person who answered a question (docs/design/chat-collaboration.md
-- §6.1, in control-plane); resolved_at already says when. NULL on an answered
-- row means the chat's owner, which every row answered before this column is;
-- it is also NULL while pending and on a row the system resolved.
ALTER TABLE questions
    ADD COLUMN answered_by text;

COMMENT ON COLUMN questions.answered_by IS
    'The user who answered; NULL on an answered row means the owner. Also NULL while pending or when the system resolved it.';
