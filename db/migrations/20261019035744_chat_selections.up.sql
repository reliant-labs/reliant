-- forge:backward-compatible — additive table; the previous release never reads or writes it.

-- The composer's choice for a chat (model, mode, tools, ... as nested workflow
-- params) and the chat's provider policy, kept on the server so every run-
-- building path layers it under a send's params. Until now the choice lived only
-- in one browser profile's localStorage, so a send from another device, a
-- restart, a branch or an API call fell back to the preset's tier.
--
-- 1:1 with the chat and a separate table so a composer edit does not churn the
-- chats row that chats_with_activity (the sidebar's hot query) reads. `version`
-- is the optimistic-concurrency token: a write that names a stale version is
-- refused and the client refetches.
CREATE TABLE chat_selections (
    chat_id         text PRIMARY KEY REFERENCES chats(id) ON DELETE CASCADE,
    user_id         text NOT NULL,
    workflow_name   text NOT NULL,
    params          jsonb NOT NULL DEFAULT '{}'::jsonb,
    provider_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    version         bigint NOT NULL DEFAULT 1,
    updated_at      timestamp with time zone NOT NULL
);
