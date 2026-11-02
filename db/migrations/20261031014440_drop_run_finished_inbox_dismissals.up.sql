-- forge:backward-compatible — data only, no schema change: it deletes dismissal rows of an Inbox item kind this release no longer lists. The previous release reads the same table unchanged; on a rollback it would only show again a successful run its owner had dismissed.

-- A run an automation launched that completed successfully is no longer an
-- Inbox item, whatever its automation's notify_on_complete says (owner,
-- 2026-10-31: "I don't need successful runs showing up in my inbox"). The
-- Inbox is derived on every read, so the success-only items themselves are
-- gone with the query branch that produced them (INBOX_ITEM_KIND_RUN_FINISHED,
-- ids "run_finished:<chat id>"). What remains stored is the dismissals of
-- those items, which can now never match anything. Only that prefix is
-- touched: every other kind's dismissals, and every chat, run and unread mark,
-- are left as they are.
DELETE FROM inbox_dismissals WHERE item_id LIKE 'run_finished:%';
