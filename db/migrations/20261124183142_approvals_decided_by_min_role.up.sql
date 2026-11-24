-- forge:backward-compatible — two nullable columns the previous release never reads or writes; a NULL min_role means owner-only, the most restrictive answer, so a row an older worker writes fails closed.

-- Migration: approvals_decided_by_min_role
--
-- decided_by is the person who approved or denied (resolved_at already says
-- when). NULL on a decided row means the chat's owner, which is what every
-- row decided before this column is; it is also NULL while pending and on a
-- row the system resolved (a timeout, an interrupt).
--
-- min_role is the least chat level that may decide this approval: 'reply',
-- 'manage' or 'owner' (docs/design/chat-collaboration.md §2.4, in
-- control-plane). It is set once, when the approval is created, by
-- internal/approvalkind, and never changes. NULL means 'owner': every row
-- written before this column, or by a worker that predates it, is owner-only,
-- so a mixed-version fleet fails closed. There is deliberately no CHECK: the
-- reader (approvalkind) treats any value it does not know as 'owner' too.
ALTER TABLE approvals
    ADD COLUMN decided_by text,
    ADD COLUMN min_role text;

COMMENT ON COLUMN approvals.decided_by IS
    'The user who approved or denied; NULL on a decided row means the owner. Also NULL while pending or when the system resolved it.';
COMMENT ON COLUMN approvals.min_role IS
    'The least chat level that may decide it (reply, manage, owner), set at creation by internal/approvalkind. NULL or unknown means owner.';
