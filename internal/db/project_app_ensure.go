package db

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
	"github.com/reliant-labs/reliant/internal/store"
)

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
func (r *Repo) EnsureProjectForApp(ctx context.Context, in EnsureProjectForAppInput) (EnsureProjectForAppResult, error) {
	remote := gitremote.Canonical(in.Remote)
	if remote == "" || in.OrgID == "" || in.UserID == "" || in.AppName == "" {
		return EnsureProjectForAppResult{}, errors.New("ensure project for app: org, user, app name and a remote are required")
	}
	subpath := gitremote.NormalizeSubpath(in.Subpath)
	var out EnsureProjectForAppResult
	err := r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(ensureProjectAttempts), func(ctx context.Context) error {
		out = EnsureProjectForAppResult{}
		db := r.ORM()
		if err := store.LockOrgRemote(ctx, db, in.OrgID, remote); err != nil {
			return err
		}
		_, path, _ := strings.Cut(remote, "/")
		candidates, err := store.ListOrgRemoteCandidates(ctx, db, in.OrgID, path)
		if err != nil {
			return err
		}
		for _, c := range candidates {
			if !strings.EqualFold(gitremote.Canonical(c.RemoteURL), remote) {
				continue
			}
			repoID := c.RepoID
			if repoID == "" {
				p, err := store.GetProject(ctx, db, c.ProjectID)
				if err != nil {
					return err
				}
				if repoID, err = r.insertRootRepo(ctx, p.ID, p.Name, c.RemoteURL); err != nil {
					return err
				}
			}
			out.ProjectID = c.ProjectID
			return store.InsertRepoForgeAppIfAbsent(ctx, db, repoID, subpath, in.AppName)
		}

		cloneURL := "https://" + remote
		name, path, err := r.freeProjectName(ctx, in.UserID, in.OrgID, in.AppName)
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
		taken, err := store.UserHasProjectRemoteURL(ctx, db, in.UserID, cloneURL)
		if err != nil {
			return err
		}
		if !taken {
			project.RemoteURL = &cloneURL
		}
		if err := store.InsertProject(ctx, db, project); err != nil {
			return err
		}
		repoID, err := r.insertRootRepo(ctx, project.ID, name, cloneURL)
		if err != nil {
			return err
		}
		out = EnsureProjectForAppResult{ProjectID: project.ID, Created: true}
		return store.InsertRepoForgeAppIfAbsent(ctx, db, repoID, subpath, in.AppName)
	})
	if err != nil {
		return EnsureProjectForAppResult{}, fmt.Errorf("ensure project for app: %w", err)
	}
	return out, nil
}

func (r *Repo) insertRootRepo(ctx context.Context, projectID, name, remoteURL string) (string, error) {
	now := time.Now().UTC()
	repo := &core.Repo{ID: uuid.New().String(), ProjectID: projectID, Name: name, CreatedAt: now, UpdatedAt: now}
	if remoteURL != "" {
		repo.RemoteURL = &remoteURL
	}
	if err := store.InsertRepo(ctx, r.ORM(), repo); err != nil {
		return "", err
	}
	return repo.ID, nil
}

// freeProjectName is the app's name, or "<name> (app)" when the user already
// has a project of that name in the org (or at its placeholder path), or one
// suffixed further when that is taken too. The path is the placeholder a
// cloud clone of it would use (CreateProjectFromRepo's cloudProjectPath): the
// project has no checkout yet.
func (r *Repo) freeProjectName(ctx context.Context, userID, orgID, appName string) (string, string, error) {
	for i := 0; ; i++ {
		name := appName
		switch {
		case i == 1:
			name = appName + " (app)"
		case i > 1:
			name = fmt.Sprintf("%s (app %d)", appName, i)
		}
		path := "/home/workspace/projects/" + name
		taken, err := store.ProjectNameOrPathTaken(ctx, r.ORM(), userID, orgID, name, path)
		if err != nil {
			return "", "", err
		}
		if !taken {
			return name, path, nil
		}
	}
}
