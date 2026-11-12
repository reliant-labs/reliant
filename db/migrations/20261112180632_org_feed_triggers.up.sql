-- forge:backward-compatible — a function and triggers that only append to org_updates; the previous release never sets visibility, so on its writes the triggers fire only for rows already shared, and appending a feed row changes nothing it reads.

-- Migration: org_feed_triggers
--
-- The org feed's writers (control-plane docs/design/org-sharing.md §5.2). The
-- database writes org_updates itself, in the transaction of the change it
-- reports, so no writer of a chat or project (the share rpcs, rename,
-- archive, deletion, every activity announcement) can change a shared row
-- without its feed row, and no change of a private row writes one:
--
--   chats / projects, UPDATE of visibility: SHARED (now 'org') or UNSHARED
--     (now private, a bare marker), in the row's org.
--   chats, UPDATE of title or state / projects, UPDATE of name, while 'org':
--     CHANGED, with the new title.
--   chats / projects, DELETE of a row that was 'org': DELETED (a bare marker).
--   user_updates, INSERT of a CHAT_ACTIVITY_CHANGED (6) about a shared chat:
--     CHANGED, carrying that update's data (chat id, activity, working
--     threads, timestamp: its coarse state, nothing of the transcript),
--     which StreamOrgUpdates reads the activity from.
--
-- Each appends through org_feed_append, which takes the org's next sequence
-- number from update_stream_counters ('org', admitted by
-- 20261112180335_update_stream_counters_org_kind) under its row lock, so a
-- feed's sequence has no gaps and its order is commit order per org. A
-- message, a tool call or a token never writes the feed.
CREATE FUNCTION org_feed_append(p_org text, p_type integer, p_kind text, p_id text, p_owner text, p_project text, p_title text, p_data text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  seq bigint;
BEGIN
  IF p_org IS NULL OR p_org = '' THEN
    RETURN;
  END IF;
  INSERT INTO update_stream_counters (stream_kind, stream_id, last_assigned_seq)
  VALUES ('org', p_org, 1)
  ON CONFLICT (stream_kind, stream_id)
  DO UPDATE SET last_assigned_seq = update_stream_counters.last_assigned_seq + 1
  RETURNING last_assigned_seq INTO seq;
  INSERT INTO org_updates (id, org_id, sequence_number, update_type, resource_kind, resource_id, owner_user_id, project_id, title, data)
  VALUES (p_org || '-' || seq, p_org, seq, p_type, p_kind, p_id, p_owner, p_project, p_title, COALESCE(p_data, '{}'));
END $$;

-- OrgUpdateType: 1 SHARED, 2 UNSHARED, 3 CHANGED, 4 DELETED.
CREATE FUNCTION org_feed_chats() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.visibility = 'org' THEN
      PERFORM org_feed_append(OLD.org_id, 4, 'chat', OLD.id, OLD.user_id, OLD.project_id, NULL, NULL);
    END IF;
    RETURN OLD;
  END IF;
  IF OLD.visibility IS DISTINCT FROM NEW.visibility THEN
    IF NEW.visibility = 'org' THEN
      PERFORM org_feed_append(NEW.org_id, 1, 'chat', NEW.id, NEW.user_id, NEW.project_id, NEW.title, NULL);
    ELSE
      PERFORM org_feed_append(OLD.org_id, 2, 'chat', OLD.id, OLD.user_id, OLD.project_id, NULL, NULL);
    END IF;
  ELSIF NEW.visibility = 'org' AND (OLD.title IS DISTINCT FROM NEW.title OR OLD.state IS DISTINCT FROM NEW.state) THEN
    PERFORM org_feed_append(NEW.org_id, 3, 'chat', NEW.id, NEW.user_id, NEW.project_id, NEW.title, NULL);
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION org_feed_projects() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.visibility = 'org' THEN
      PERFORM org_feed_append(OLD.org_id, 4, 'project', OLD.id, OLD.user_id, NULL, NULL, NULL);
    END IF;
    RETURN OLD;
  END IF;
  IF OLD.visibility IS DISTINCT FROM NEW.visibility THEN
    IF NEW.visibility = 'org' THEN
      PERFORM org_feed_append(NEW.org_id, 1, 'project', NEW.id, NEW.user_id, NULL, NEW.name, NULL);
    ELSE
      PERFORM org_feed_append(OLD.org_id, 2, 'project', OLD.id, OLD.user_id, NULL, NULL, NULL);
    END IF;
  ELSIF NEW.visibility = 'org' AND OLD.name IS DISTINCT FROM NEW.name THEN
    PERFORM org_feed_append(NEW.org_id, 3, 'project', NEW.id, NEW.user_id, NULL, NEW.name, NULL);
  END IF;
  RETURN NEW;
END $$;

-- One primary-key read per activity announcement, only for chats.
CREATE FUNCTION org_feed_chat_activity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  c record;
BEGIN
  SELECT id, org_id, user_id, project_id, title, visibility INTO c FROM chats WHERE id = NEW.chat_id;
  IF FOUND AND c.visibility = 'org' THEN
    PERFORM org_feed_append(c.org_id, 3, 'chat', c.id, c.user_id, c.project_id, c.title, NEW.data);
  END IF;
  RETURN NULL;
END $$;

CREATE TRIGGER org_feed_chats_update AFTER UPDATE OF visibility, title, state ON chats
  FOR EACH ROW EXECUTE FUNCTION org_feed_chats();
CREATE TRIGGER org_feed_chats_delete BEFORE DELETE ON chats
  FOR EACH ROW WHEN (OLD.visibility = 'org') EXECUTE FUNCTION org_feed_chats();
CREATE TRIGGER org_feed_projects_update AFTER UPDATE OF visibility, name ON projects
  FOR EACH ROW EXECUTE FUNCTION org_feed_projects();
CREATE TRIGGER org_feed_projects_delete BEFORE DELETE ON projects
  FOR EACH ROW WHEN (OLD.visibility = 'org') EXECUTE FUNCTION org_feed_projects();
CREATE TRIGGER org_feed_chat_activity AFTER INSERT ON user_updates
  FOR EACH ROW WHEN (NEW.update_type = 6 AND NEW.chat_id IS NOT NULL) EXECUTE FUNCTION org_feed_chat_activity();
