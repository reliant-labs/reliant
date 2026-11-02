-- forge:backward-compatible — adds a nullable column; the previous release neither reads nor writes it
-- Migration: approval_batch
--
-- An approval raised together with others by the same run step (one turn's
-- activation changes and machine starts) carries the batch's id, so the chat
-- and the Inbox can offer them as one decision. NULL for an approval raised
-- on its own (a workflow approval node).
ALTER TABLE approvals ADD COLUMN batch_id text;
