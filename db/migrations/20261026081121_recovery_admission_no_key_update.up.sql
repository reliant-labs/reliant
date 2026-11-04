-- forge:backward-compatible — replaces two trigger function bodies with the same checks under a weaker row lock; no table, column or signature changes.
--
-- The recovery admission triggers (20261026024920_git_worktree_recovery) lock
-- the chat's worktree row to fence a recovery claim. They took FOR UPDATE,
-- the one row lock that also conflicts with FOR KEY SHARE, which every
-- foreign-key check against worktrees takes (threads, user_updates,
-- background_processes, triggers). So two concurrent StartChats in one
-- worktree deadlocked (SQLSTATE 40P01): the launch held the worktree row and
-- waited for the user's update-sequence counter, while the previous launch's
-- post-commit activity announcement held that counter and waited, in its
-- user_updates insert's foreign-key check, for a KEY SHARE on the worktree.
--
-- FOR NO KEY UPDATE keeps every exclusion the fence needs and drops that one:
-- it conflicts with another admission's FOR NO KEY UPDATE and with the
-- recovery claim's FOR UPDATE (worktree_recovery_repo_ext.go), so a claim
-- and an admission still serialize, but a foreign-key check no longer waits
-- behind an admission. The UPDATE of activity_generation that follows takes
-- FOR NO KEY UPDATE itself, so the lock the triggers hold is unchanged from
-- that statement on. The application's own admission lock, taken before the
-- chat write (store.withChatWorktreeAdmission), moves to the same mode.

CREATE OR REPLACE FUNCTION recovery_chat_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE finalizing text; oldwt text;
BEGIN
 IF TG_OP='UPDATE' THEN oldwt := OLD.worktree_id; END IF;
 -- Lock both ends in stable order, including archived rebindings. Moving a
 -- binding away must not hide an archived-chat run from an in-flight claim.
 FOR finalizing IN SELECT recovery_finalizing_id FROM worktrees
   WHERE id IN (NEW.worktree_id,oldwt) ORDER BY id FOR NO KEY UPDATE LOOP
   IF finalizing IS NOT NULL THEN
     RAISE EXCEPTION 'worktree is finalizing recovery' USING ERRCODE = '55P03';
   END IF;
 END LOOP;
 IF NEW.worktree_id IS NOT NULL AND NEW.state <> 3 THEN
   UPDATE worktrees SET activity_generation = activity_generation + 1 WHERE id = NEW.worktree_id;
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION recovery_workflow_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE wt text; finalizing text;
BEGIN
 IF NEW.state IN (1,2) OR (NEW.state=3 AND NEW.stop_reason=3) THEN
   SELECT worktree_id INTO wt FROM chats WHERE id=NEW.chat_id;
   IF wt IS NOT NULL THEN
     SELECT recovery_finalizing_id INTO finalizing FROM worktrees WHERE id=wt FOR NO KEY UPDATE;
     IF finalizing IS NOT NULL THEN
       RAISE EXCEPTION 'worktree is finalizing recovery' USING ERRCODE='55P03';
     END IF;
     UPDATE worktrees SET activity_generation=activity_generation+1 WHERE id=wt;
   END IF;
 END IF;
 RETURN NEW;
END $$;
