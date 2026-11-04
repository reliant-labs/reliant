-- forge:backward-compatible — additive catalog/admission; old writes participate through database triggers.
ALTER TABLE worktrees ADD COLUMN activity_generation bigint NOT NULL DEFAULT 0 CHECK (activity_generation >= 0);
ALTER TABLE worktrees ADD COLUMN recovery_finalizing_id text;

-- Deliberately NO source/owner/project/daemon FK: source deletion must never erase recovery.
CREATE TABLE worktree_recoveries (
 id text PRIMARY KEY,
 owner_user_id text NOT NULL,
 source_worktree_id text NOT NULL,
 project_id text NOT NULL,
 daemon_id text NOT NULL,
 capture_id text NOT NULL,
 fence text NOT NULL,
 activity_generation bigint NOT NULL CHECK (activity_generation >= 0),
 observation_id text NOT NULL,
 manifest_digest text NOT NULL,
 phase text NOT NULL DEFAULT 'capturing',
 last_error text NOT NULL DEFAULT '',
 idempotency_key text NOT NULL,
 operation_key text NOT NULL DEFAULT '',
 operation text NOT NULL DEFAULT '',
 target_daemon_id text NOT NULL DEFAULT '',
 authorized_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_user_id, idempotency_key),
 UNIQUE(source_worktree_id, capture_id)
);
CREATE INDEX worktree_recoveries_owner ON worktree_recoveries(owner_user_id, project_id, created_at);
CREATE TABLE worktree_recovery_checkouts (
 recovery_id text NOT NULL,
 repo_id text NOT NULL,
 rel text NOT NULL,
 remote_url text NOT NULL,
 recovery_ref text NOT NULL,
 commit_oid text NOT NULL,
 manifest_digest text NOT NULL,
 descriptor_json text NOT NULL,
 receipt_json text NOT NULL DEFAULT '',
 phase text NOT NULL DEFAULT 'capturing',
 last_error text NOT NULL DEFAULT '',
 PRIMARY KEY(recovery_id, repo_id),
 UNIQUE(recovery_id, rel),
 UNIQUE(remote_url, recovery_ref)
);

-- Every writer, including generated CRUD and future writers, takes the same
-- parent lock BEFORE a live binding is admitted. No handler-only race window.
CREATE FUNCTION recovery_chat_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE finalizing text; oldwt text;
BEGIN
 IF TG_OP='UPDATE' THEN oldwt := OLD.worktree_id; END IF;
 -- Lock both ends in stable order, including archived rebindings. Moving a
 -- binding away must not hide an archived-chat run from an in-flight claim.
 FOR finalizing IN SELECT recovery_finalizing_id FROM worktrees
   WHERE id IN (NEW.worktree_id,oldwt) ORDER BY id FOR UPDATE LOOP
   IF finalizing IS NOT NULL THEN
     RAISE EXCEPTION 'worktree is finalizing recovery' USING ERRCODE = '55P03';
   END IF;
 END LOOP;
 IF NEW.worktree_id IS NOT NULL AND NEW.state <> 3 THEN
   UPDATE worktrees SET activity_generation = activity_generation + 1 WHERE id = NEW.worktree_id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER recovery_chat_admission BEFORE INSERT OR UPDATE OF worktree_id, state ON chats
 FOR EACH ROW EXECUTE FUNCTION recovery_chat_admission();

CREATE FUNCTION recovery_worktree_admission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.recovery_finalizing_id IS NOT NULL AND
    (NEW.deleted_at IS DISTINCT FROM OLD.deleted_at OR NEW.last_active IS DISTINCT FROM OLD.last_active
     OR NEW.path IS DISTINCT FROM OLD.path OR NEW.project_id IS DISTINCT FROM OLD.project_id
     OR NEW.daemon_id IS DISTINCT FROM OLD.daemon_id OR NEW.is_main IS DISTINCT FROM OLD.is_main) THEN
   RAISE EXCEPTION 'worktree is finalizing recovery' USING ERRCODE = '55P03';
 END IF;
 IF (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL) OR NEW.last_active IS DISTINCT FROM OLD.last_active THEN
   NEW.activity_generation := OLD.activity_generation + 1;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER recovery_worktree_admission BEFORE UPDATE ON worktrees
 FOR EACH ROW EXECUTE FUNCTION recovery_worktree_admission();
CREATE FUNCTION recovery_worktree_delete_admission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.recovery_finalizing_id IS NOT NULL THEN
   RAISE EXCEPTION 'worktree is finalizing recovery' USING ERRCODE='55P03';
 END IF;
 RETURN OLD;
END $$;
CREATE TRIGGER recovery_worktree_delete_admission BEFORE DELETE ON worktrees
 FOR EACH ROW EXECUTE FUNCTION recovery_worktree_delete_admission();

-- Pending/running/paused work remains admitted even when its chat is archived.
-- State and stop_reason values are the wire enums (pending=1,active=2,
-- stopped=3,paused=3). Completion writes always remain possible.
CREATE FUNCTION recovery_workflow_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE wt text; finalizing text;
BEGIN
 IF NEW.state IN (1,2) OR (NEW.state=3 AND NEW.stop_reason=3) THEN
   SELECT worktree_id INTO wt FROM chats WHERE id=NEW.chat_id;
   IF wt IS NOT NULL THEN
     SELECT recovery_finalizing_id INTO finalizing FROM worktrees WHERE id=wt FOR UPDATE;
     IF finalizing IS NOT NULL THEN
       RAISE EXCEPTION 'worktree is finalizing recovery' USING ERRCODE='55P03';
     END IF;
     UPDATE worktrees SET activity_generation=activity_generation+1 WHERE id=wt;
   END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER recovery_workflow_admission BEFORE INSERT OR UPDATE OF state,stop_reason,chat_id ON workflows
 FOR EACH ROW EXECUTE FUNCTION recovery_workflow_admission();
