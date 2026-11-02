-- forge:backward-compatible — data only, no schema change: it deletes project_daemons rows whose machine no longer exists or belongs to a user other than the project's. No release can run anything on such a machine; the previous release reads the same table and simply stops seeing rows it could never use.

-- Migration: project_daemons_drop_orphans
--
-- A project_daemons row says "this machine holds the project's checkout, at
-- this path". Removing a machine (db.Repo.RemoveDaemon) now deletes its rows
-- in the same transaction (store.ReleaseDaemonFromWorkspaces), but rows of
-- machines removed before that, or whose id churned, were left behind. Such a
-- row named a machine nothing can reach, and the plain-folder heal read it as
-- "the folder lives on another machine", so a replaced machine never got its
-- plain folders back: onboarding's my-project, which has no remote to clone
-- from, stayed missing for good.
--
-- The readers already ignore a row whose machine is gone or is another
-- user's (daemonplacement.UserCheckouts); this removes the rows themselves,
-- so nothing that reads the table directly can trip on one.
DELETE FROM project_daemons pd
WHERE NOT EXISTS (
    SELECT 1
    FROM daemons d
    JOIN projects p ON p.id = pd.project_id
    WHERE d.id = pd.daemon_id
      AND d.user_id = p.user_id
);
