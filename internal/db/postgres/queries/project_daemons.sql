-- name: UpsertProjectDaemon :exec
-- Records a COMPLETED clone. install_state is forced to 'installed' rather
-- than left to the column default, so a row that was previously 'installing'
-- or 'failed' is corrected when the clone finally lands.
INSERT INTO project_daemons (
    project_id, daemon_id, path, default_branch, cloned_at, install_state
) VALUES ($1, $2, $3, $4, NOW(), 'installed')
ON CONFLICT (project_id, daemon_id) DO UPDATE SET
    path = EXCLUDED.path,
    default_branch = EXCLUDED.default_branch,
    install_state = 'installed',
    install_error = '',
    install_request_id = '';

-- name: UpsertQueuedProjectDaemon :exec
-- Records a clone that has been QUEUED for a daemon but has not run. The path
-- is where the checkout WILL be, not where it is. install_request_id ties the
-- row to the queued command so the outcome can find it.
INSERT INTO project_daemons (
    project_id, daemon_id, path, default_branch, cloned_at,
    install_state, install_error, install_request_id
) VALUES ($1, $2, $3, $4, NOW(), 'installing', '', $5)
ON CONFLICT (project_id, daemon_id) DO UPDATE SET
    path = EXCLUDED.path,
    default_branch = EXCLUDED.default_branch,
    install_state = 'installing',
    install_error = '',
    install_request_id = EXCLUDED.install_request_id;

-- name: MarkProjectDaemonInstallFailed :exec
-- The daemon reported the queued clone failed. Matched on request id because
-- the failure notification carries no project id.
UPDATE project_daemons
SET install_state = 'failed',
    install_error = $2
WHERE install_request_id = $1 AND install_request_id <> '';

-- name: MarkProjectDaemonInstalledByRequest :exec
-- The daemon reported the queued clone succeeded.
UPDATE project_daemons
SET install_state = 'installed',
    install_error = '',
    cloned_at = NOW()
WHERE install_request_id = $1 AND install_request_id <> '';

-- name: ListProjectDaemonsForProject :many
SELECT project_id, daemon_id, path, default_branch, cloned_at,
       install_state, install_error, install_request_id
FROM project_daemons
WHERE project_id = $1
ORDER BY cloned_at ASC;

-- name: ListProjectDaemonsForDaemon :many
SELECT project_id, daemon_id, path, default_branch, cloned_at,
       install_state, install_error, install_request_id
FROM project_daemons
WHERE daemon_id = $1
ORDER BY cloned_at ASC;

-- name: DeleteProjectDaemon :exec
DELETE FROM project_daemons
WHERE project_id = $1 AND daemon_id = $2;

-- name: MarkProjectDaemonInstalled :exec
-- Settles a queued clone from the daemon's filesystem announcement, which
-- carries the path but no request id. A no-op for rows already installed.
UPDATE project_daemons
SET install_state = 'installed',
    install_error = '',
    cloned_at = NOW()
WHERE project_id = $1 AND daemon_id = $2 AND install_state <> 'installed';
