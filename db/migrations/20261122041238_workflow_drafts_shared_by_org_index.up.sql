-- forge:backward-compatible — a new partial index; no statement of the previous release changes meaning, and the index covers only rows (visibility = 'org') that release never writes.

-- Migration: workflow_drafts_shared_by_org_index
-- Created: 2026-11-22T04:12:38Z
--
-- ListSharedWorkflowDrafts reads an org's shared saved workflows, most
-- recently shared first (control-plane docs/design/org-sharing.md §8.5), as a
-- partial index for the same reason as chats_shared_by_org: shared rows are a
-- small slice of workflow_drafts, and idx_workflow_drafts_user_org leads with
-- user_id, so it cannot serve a read by org.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file. IF NOT EXISTS makes a retry after a failed build a
-- no-op only when the index is valid; an INVALID leftover is dropped by hand
-- (DROP INDEX CONCURRENTLY workflow_drafts_shared_by_org) before the retry.
CREATE INDEX CONCURRENTLY IF NOT EXISTS workflow_drafts_shared_by_org ON public.workflow_drafts (org_id, shared_at DESC) WHERE visibility = 'org';
