-- forge:backward-compatible — catalog comments only; no table, column or view definition changes, and the previous release never reads comments.

-- Integer columns of chats_with_activity that hold an enum project to that
-- type on the generated read-only entity (internal/store), so the
-- conversation lane (P5b L1b) filters on named values — the inbox lists
-- RunDisplayWaitingForMachine, not 8 — and maps the field without a cast.
--
-- activity is the view's computed reliantv1.ChatActivity; display_state is
-- the single run status a user reads, the values core.RunDisplayState names
-- (the reliantv1.RunDisplayState wire values). chats itself has no activity
-- column: activity is derived, and only the view carries it.
--
-- A view's comments are dropped with the view, so a migration that DROPs and
-- re-CREATEs chats_with_activity must repeat these two statements along with
-- the view block of 20261020232630_chats_with_activity_view_entity.up.sql.
COMMENT ON COLUMN chats_with_activity.activity IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.ChatActivity';
COMMENT ON COLUMN chats_with_activity.display_state IS 'forge:go-type=github.com/reliant-labs/reliant/internal/db/core;core.RunDisplayState';
