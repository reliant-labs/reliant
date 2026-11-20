package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"
)

// The queries behind db.Repo.EnsureProjectForApp: control-plane's "a deployed
// app gets a linked project in its org" (grpc.EnsureProjectForAppPath).

// LockOrgRemote takes the transaction-scoped advisory lock that serializes
// every ensure of one canonical remote in one org, so two concurrent deploys
// of the same repository find-or-create one project, never two. ctx must
// carry a transaction.
func LockOrgRemote(ctx context.Context, db orm.Context, orgID, canonicalRemote string) error {
	key := "org-remote-project:" + orgID + ":" + strings.ToLower(canonicalRemote)
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("failed to lock the org's remote: %w", err)
	}
	return nil
}

// OrgRemoteCandidate is one repo of a live project in an org, with the remote
// it is a checkout of: the repo's own, else (for the root repo, or a project
// with no repo rows) the project's. RepoID is "" for a project with no repos.
type OrgRemoteCandidate struct {
	ProjectID string `bun:"project_id"`
	RepoID    string `bun:"repo_id"`
	RemoteURL string `bun:"remote"`
}

// ListOrgRemoteCandidates returns the repos of every live project in orgID
// (any owner, any visibility; delete_after IS NULL) whose remote contains
// pathHint, case-insensitively: a coarse prefilter, the caller compares the
// canonical forms. Oldest project first, its root repo first.
func ListOrgRemoteCandidates(ctx context.Context, db orm.Context, orgID, pathHint string) ([]OrgRemoteCandidate, error) {
	var rows []OrgRemoteCandidate
	if err := db.Bun().NewRaw(`SELECT p.id AS project_id, COALESCE(r.id, '') AS repo_id, c.remote
FROM projects p
LEFT JOIN repos r ON r.project_id = p.id
CROSS JOIN LATERAL (SELECT COALESCE(r.remote_url, CASE WHEN r.id IS NULL OR r.relative_path = '' THEN p.remote_url END) AS remote) c
WHERE p.org_id = ?0 AND p.delete_after IS NULL AND c.remote ILIKE ?1 ESCAPE '\'
ORDER BY p.created_at, p.id, r.relative_path NULLS FIRST`, orgID, "%"+likeEscape(pathHint)+"%").Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list org remote candidates: %w", err)
	}
	return rows, nil
}

// ProjectNameOrPathTaken reports whether userID already has a project named
// name in orgID, or one at path in any org (the (user_id, path) key spans
// orgs until its contract half lands).
func ProjectNameOrPathTaken(ctx context.Context, db orm.Context, userID, orgID, name, path string) (bool, error) {
	var n []int
	if err := db.Bun().NewRaw(`SELECT 1 FROM projects
WHERE user_id = ?0 AND ((org_id = ?1 AND name = ?2) OR path = ?3) LIMIT 1`, userID, orgID, name, path).Scan(ctx, &n); err != nil {
		return false, fmt.Errorf("check project name: %w", err)
	}
	return len(n) > 0, nil
}

// UserHasProjectRemoteURL reports whether userID owns a project with exactly
// this remote_url in any org (projects_user_remote_url_uniq spans orgs).
func UserHasProjectRemoteURL(ctx context.Context, db orm.Context, userID, remoteURL string) (bool, error) {
	var n []int
	if err := db.Bun().NewRaw(`SELECT 1 FROM projects WHERE user_id = ?0 AND remote_url = ?1 LIMIT 1`,
		userID, remoteURL).Scan(ctx, &n); err != nil {
		return false, fmt.Errorf("check project remote: %w", err)
	}
	return len(n) > 0, nil
}

// InsertRepoForgeAppIfAbsent records that the repo holds forge app name at
// subpath unless a row for (repo, subpath) exists: a daemon's scan of the
// checkout is the authority, a deploy's report only fills the gap.
func InsertRepoForgeAppIfAbsent(ctx context.Context, db orm.Context, repoID, subpath, name string) error {
	if _, err := db.Bun().NewRaw(`INSERT INTO repo_forge_apps (repo_id, subpath, forge_name) VALUES (?0, ?1, ?2)
ON CONFLICT (repo_id, subpath) DO NOTHING`, repoID, subpath, name).Exec(ctx); err != nil {
		return fmt.Errorf("insert repo forge app: %w", err)
	}
	return nil
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
