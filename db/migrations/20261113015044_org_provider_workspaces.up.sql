-- forge:backward-compatible — adds a new table the previous release never reads or writes.

-- Migration: org_provider_workspaces
-- Created: 2026-11-13T01:50:44Z
--
-- A Claude Team/Enterprise or ChatGPT Business/Team workspace an org pays for
-- (org-provider-accounts.md §3.4). An admin binds it from one of their own
-- team-plan sign-ins, never by typing an id: external_org_id is read from that
-- account's provider-issued identity (Claude organization.uuid, or the Codex
-- JWT's chatgpt_account_id). Members' sign-ins in the workspace are then
-- attributed to the org (provider_accounts.attributed_org_id, source
-- 'workspace').
--
-- UNIQUE (provider, external_org_id): one workspace pays for one Reliant org.
-- bound_by_user_id is attribution only. Nothing writes this table yet.
CREATE TABLE org_provider_workspaces (
    org_id           text NOT NULL,
    provider         text NOT NULL CHECK (provider IN ('claude', 'codex')),
    external_org_id  text NOT NULL,
    display_name     text NOT NULL,
    bound_by_user_id text NOT NULL,
    bound_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, provider, external_org_id),
    CONSTRAINT org_provider_workspaces_one_org UNIQUE (provider, external_org_id),
    CONSTRAINT org_provider_workspaces_ids_nonempty CHECK (org_id <> '' AND external_org_id <> '' AND bound_by_user_id <> '')
);
