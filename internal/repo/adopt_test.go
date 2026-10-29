// Copyright (c) 2025 Reliant Labs
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// pinRecorder is a PinnedSender that records which entry point a command
// took, and answers repo.discover with a canned result or error.
type pinRecorder struct {
	discovered []map[string]any
	err        error

	defaultCalls int
	pinnedTo     []string
}

func (p *pinRecorder) answer() ([]byte, error) {
	if p.err != nil {
		return nil, p.err
	}
	return json.Marshal(map[string]any{"discovered": p.discovered})
}

func (p *pinRecorder) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	p.defaultCalls++
	return p.answer()
}

func (p *pinRecorder) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, _ string, _ []byte, _ int32) ([]byte, error) {
	p.pinnedTo = append(p.pinnedTo, daemonID)
	return p.answer()
}

// memAdoptStore is an in-memory AdoptStore over MockAdoptStore.
func memAdoptStore() (*MockAdoptStore, *[]*core.Repo) {
	var rows []*core.Repo
	return &MockAdoptStore{
		GetRepoByProjectAndPathFunc: func(_ context.Context, projectID, rel string) (*core.Repo, error) {
			for _, r := range rows {
				if r.ProjectID == projectID && r.RelativePath == rel {
					return r, nil
				}
			}
			return nil, errors.New("not found")
		},
		CreateRepoFunc: func(_ context.Context, r *core.Repo) error {
			rows = append(rows, r)
			return nil
		},
		ListReposByProjectFunc: func(context.Context, string) ([]*core.Repo, error) {
			return rows, nil
		},
		UpdateProjectFunc: func(context.Context, *db.Project, string) error { return nil },
	}, &rows
}

func adoptProject() *db.Project {
	return &db.Project{ID: "p1", UserID: "u1", Name: "control-plane", Path: "/home/workspace/projects/control-plane"}
}

// The project's checkout lives on one machine; default resolution prefers a
// connected local daemon, which has no checkout at the cloud path. Adoption
// must ask the machine it is bound to.
func TestAdoptFromDaemon_AsksTheBoundMachine(t *testing.T) {
	router := &pinRecorder{discovered: []map[string]any{{"relative_path": "", "name": "control-plane", "remote_url": "https://github.com/acme/cp.git"}}}
	store, rows := memAdoptStore()
	project := adoptProject()

	repos, err := AdoptFromDaemon(context.Background(), store, OnDaemon(router, "cloud-machine"), project)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "", repos[0].RelativePath)
	assert.Equal(t, []string{"cloud-machine"}, router.pinnedTo)
	assert.Zero(t, router.defaultCalls, "a bound adoption never falls back to default resolution")
	require.Len(t, *rows, 1)
	assert.True(t, project.IsGitRepo)
}

func TestAdoptFromDaemon_UnboundUsesDefaultResolution(t *testing.T) {
	router := &pinRecorder{discovered: []map[string]any{{"relative_path": "api"}}}
	store, _ := memAdoptStore()

	repos, err := AdoptFromDaemon(context.Background(), store, OnDaemon(router, ""), adoptProject())
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "api", repos[0].Name)
	assert.Equal(t, 1, router.defaultCalls)
	assert.Empty(t, router.pinnedTo)
}

// A machine that could not answer is not a machine that found nothing: the
// caller must be able to tell the two apart, or a real repo is reported as
// "no git repos".
func TestAdoptFromDaemon_DiscoverFailureIsAnError(t *testing.T) {
	cause := errors.New("daemon command repo.discover: nats: no responders available for request")
	router := &pinRecorder{err: cause}
	store, rows := memAdoptStore()

	repos, err := AdoptFromDaemon(context.Background(), store, OnDaemon(router, "m"), adoptProject())
	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "/home/workspace/projects/control-plane")
	assert.Nil(t, repos)
	assert.Empty(t, *rows)
}

func TestAdoptFromDaemon_NothingOnDiskIsNoErrorAndNoRepos(t *testing.T) {
	router := &pinRecorder{}
	store, rows := memAdoptStore()

	repos, err := AdoptFromDaemon(context.Background(), store, OnDaemon(router, "m"), adoptProject())
	require.NoError(t, err)
	assert.Empty(t, repos)
	assert.Empty(t, *rows)
}

func TestNoReposMessageNamesTheFolder(t *testing.T) {
	msg := NoReposMessage(adoptProject())
	assert.Contains(t, msg, "/home/workspace/projects/control-plane")
	assert.Contains(t, msg, "git init")
	assert.NotContains(t, msg, "project has no git repos")
}
