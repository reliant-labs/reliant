-- forge:backward-compatible — adds one nullable column with no default; the previous release neither reads nor writes it, and its inserts leave it NULL.

-- Migration: messages_thinking_level
--
-- The effective thinking (reasoning-effort) level an assistant turn ran at,
-- after the model's capabilities reconciled the requested level. Until now it
-- was recorded nowhere, so "this step ran at high" could not be verified.
-- NULL for messages no model wrote, models that take no level, and every
-- message saved before this column existed.
ALTER TABLE messages ADD COLUMN thinking_level TEXT;
