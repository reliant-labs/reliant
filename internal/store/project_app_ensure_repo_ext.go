package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitremote"
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

// LockUserProjectNames takes the transaction-scoped advisory lock that
// serializes every ensure's choice of a free project name for one user, so two
// ensures of different remotes (whose LockOrgRemote locks differ) never pick
// the same free name or placeholder path. Taken after LockOrgRemote, in that
// order by every ensure. ctx must carry a transaction.
func LockUserProjectNames(ctx context.Context, db orm.Context, userID string) error {
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "user-project-names:"+userID); err != nil {
		return fmt.Errorf("failed to lock the user's project names: %w", err)
	}
	return nil
}

// ListOrgRemoteCandidates returns the repos of every live project in orgID
// (any owner, any visibility; delete_after IS NULL) that has a remote, for the
// caller to compare in canonical form. There is no SQL prefilter on the
// remote's text: a remote can be spelled in any form gitremote.Canonical
// accepts (scp-style, userinfo, a port, a different case or suffix), and a
// filter that missed one would create a duplicate project instead of reusing
// the live one. Oldest project first, its root repo first.
func ListOrgRemoteCandidates(ctx context.Context, db orm.Context, orgID string) ([]OrgRemoteCandidate, error) {
	var rows []OrgRemoteCandidate
	if err := db.Bun().NewRaw(`SELECT p.id AS project_id, COALESCE(r.id, '') AS repo_id, c.remote
FROM projects p
LEFT JOIN repos r ON r.project_id = p.id
CROSS JOIN LATERAL (SELECT COALESCE(r.remote_url, CASE WHEN r.id IS NULL OR r.relative_path = '' THEN p.remote_url END) AS remote) c
WHERE p.org_id = ?0 AND p.delete_after IS NULL AND c.remote IS NOT NULL AND c.remote <> ''
ORDER BY p.created_at, p.id, r.relative_path NULLS FIRST`, orgID).Scan(ctx, &rows); err != nil {
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

// EnsureProjectForAppInput is a deployed forge app control-plane wants a
// project for: the app's name, the canonical remote ("host/owner/repo") and
// subpath its code was deployed from, the org it was deployed into, and the
// person who deployed it (the owner of a project this creates).
type EnsureProjectForAppInput struct {
	OrgID   string
	UserID  string
	AppName string
	Remote  string
	Subpath string
}

// EnsureProjectForAppResult is the project that now links the app, and
// whether this call created it.
type EnsureProjectForAppResult struct {
	ProjectID string
	Created   bool
}

// ensureProjectAttempts bounds the re-runs of an ensure that lost a unique-key
// race with a create outside the lock (CreateProject, CreateProjectFromRepo):
// the re-run's lookup reads the winner back, or picks a free name.
const ensureProjectAttempts = 3

// EnsureProjectForApp finds the live project in in.OrgID that is a checkout of
// in.Remote — whoever owns it, whatever its visibility — or creates one owned
// by in.UserID with no checkout (no project_daemons row), and records the app
// at in.Subpath on its repo (repo_forge_apps). It never reads or writes
// outside in.OrgID; membership is the caller's check. Idempotent, and
// serialized per (org, remote) by an advisory lock, under which READ
// COMMITTED is correct.
func EnsureProjectForApp(ctx context.Context, db orm.Context, in EnsureProjectForAppInput) (EnsureProjectForAppResult, error) {
	remote := gitremote.Canonical(in.Remote)
	if remote == "" || in.OrgID == "" || in.UserID == "" || in.AppName == "" {
		return EnsureProjectForAppResult{}, errors.New("ensure project for app: org, user, app name and a remote are required")
	}
	subpath := gitremote.NormalizeSubpath(in.Subpath)
	var out EnsureProjectForAppResult
	err := db.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(ensureProjectAttempts), func(ctx context.Context) error {
		out = EnsureProjectForAppResult{}
		if err := LockOrgRemote(ctx, db, in.OrgID, remote); err != nil {
			return err
		}
		candidates, err := ListOrgRemoteCandidates(ctx, db, in.OrgID)
		if err != nil {
			return err
		}
		for _, c := range candidates {
			if !strings.EqualFold(gitremote.Canonical(c.RemoteURL), remote) {
				continue
			}
			repoID := c.RepoID
			if repoID == "" {
				p, err := GetProject(ctx, db, c.ProjectID)
				if err != nil {
					return err
				}
				if repoID, err = insertRootRepo(ctx, db, p.ID, p.Name, c.RemoteURL); err != nil {
					return err
				}
			}
			out.ProjectID = c.ProjectID
			return InsertRepoForgeAppIfAbsent(ctx, db, repoID, subpath, in.AppName)
		}

		cloneURL := "https://" + remote
		// Ensures of different remotes hold different LockOrgRemote locks;
		// this one serializes their name choice, so they never pick the same
		// free name or path. A create outside any ensure (CreateProject) can
		// still take a name first: its unique violation (23505) is retried,
		// and the re-run picks the next free name.
		if err := LockUserProjectNames(ctx, db, in.UserID); err != nil {
			return err
		}
		name, path, err := freeProjectName(ctx, db, in.UserID, in.OrgID, in.AppName)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		orgID := in.OrgID
		project := &core.Project{
			ID: uuid.New().String(), Name: name, Path: path, UserID: in.UserID, OrgID: &orgID,
			IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}
		// projects_user_remote_url_uniq is (user_id, remote_url) across orgs:
		// a user whose project in another org has this URL gets this one
		// with the remote on its root repo alone, which the app link reads.
		taken, err := UserHasProjectRemoteURL(ctx, db, in.UserID, cloneURL)
		if err != nil {
			return err
		}
		if !taken {
			project.RemoteURL = &cloneURL
		}
		if err := InsertProject(ctx, db, project); err != nil {
			return err
		}
		repoID, err := insertRootRepo(ctx, db, project.ID, name, cloneURL)
		if err != nil {
			return err
		}
		out = EnsureProjectForAppResult{ProjectID: project.ID, Created: true}
		return InsertRepoForgeAppIfAbsent(ctx, db, repoID, subpath, in.AppName)
	})
	if err != nil {
		return EnsureProjectForAppResult{}, fmt.Errorf("ensure project for app: %w", err)
	}
	return out, nil
}

func insertRootRepo(ctx context.Context, db orm.Context, projectID, name, remoteURL string) (string, error) {
	now := time.Now().UTC()
	repo := &core.Repo{ID: uuid.New().String(), ProjectID: projectID, Name: name, CreatedAt: now, UpdatedAt: now}
	if remoteURL != "" {
		repo.RemoteURL = &remoteURL
	}
	if err := InsertRepo(ctx, db, repo); err != nil {
		return "", err
	}
	return repo.ID, nil
}

// freeProjectName is the app's name, or "<name> (app)" when the user already
// has a project of that name in the org (or at its placeholder path), or one
// suffixed further when that is taken too. The path is the placeholder a
// cloud clone of it would use (CreateProjectFromRepo's cloudProjectPath): the
// project has no checkout yet.
func freeProjectName(ctx context.Context, db orm.Context, userID, orgID, appName string) (string, string, error) {
	for i := 0; ; i++ {
		name := appName
		switch {
		case i == 1:
			name = appName + " (app)"
		case i > 1:
			name = fmt.Sprintf("%s (app %d)", appName, i)
		}
		path := "/home/workspace/projects/" + name
		taken, err := ProjectNameOrPathTaken(ctx, db, userID, orgID, name, path)
		if err != nil {
			return "", "", err
		}
		if !taken {
			return name, path, nil
		}
	}
}
