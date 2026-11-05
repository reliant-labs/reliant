-- forge:backward-compatible — a new table the previous release never reads or writes.

-- Migration: user_app_sessions
--
-- One row per user who has ever made an authenticated request to the API: the
-- durable "first session" the app_first_session product event fires on
-- (internal/analytics TrackSessionStart). The auth interceptor's session tracker
-- is in memory and per replica, so it can say "first request in 30 minutes" but
-- never "first request ever"; this row can. An INSERT ... ON CONFLICT DO NOTHING
-- that inserts is the first session, exactly once, whichever replica wins.
--
-- Not an entity: it has no rpc and no CRUD; the one query is in
-- internal/store/user_app_session_repo_ext.go.
CREATE TABLE public.user_app_sessions (
    user_id text PRIMARY KEY,
    first_session_at timestamp with time zone NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.user_app_sessions IS 'forge:no-entity';

-- Users who already used the app (they own a project or a chat) had their first
-- session before this table existed: record it, dated by their earliest one, so
-- their next visit is not reported as a first session.
INSERT INTO public.user_app_sessions (user_id, first_session_at)
SELECT user_id, min(created_at)
  FROM (SELECT user_id, created_at FROM public.projects
        UNION ALL
        SELECT user_id, created_at FROM public.chats) AS used
 WHERE user_id <> ''
 GROUP BY user_id
ON CONFLICT (user_id) DO NOTHING;
