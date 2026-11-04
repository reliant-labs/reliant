-- forge:backward-compatible — additive nullable column; the previous release neither reads nor writes it, and its explicit-column chat reads ignore it.

-- Migration: queued_for_machine_selector
-- Created: 2026-10-29T09:25:58Z
--
-- The machine a chat's queued message is waiting for, as the run that queued
-- it selected it (id, name, type, labels). Delivery used to ask whether the
-- chat's pinned machine — or any of the user's machines — was connected; a run
-- that targets a different machine (a workflow-level daemon selector) then
-- failed its preflight again at once, re-queued, and was delivered again on
-- the next sweep: ~10k failed runs from one chat in 72h (prod, 2026-10-26).
-- Read only while queued_for_machine_at is set; NULL means the chat's pinned
-- machine, as before.
ALTER TABLE chats ADD COLUMN queued_for_machine_selector jsonb;
