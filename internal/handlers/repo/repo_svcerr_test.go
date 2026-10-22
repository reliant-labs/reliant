// Copyright (c) 2025 Reliant Labs
package repo_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	repo "github.com/reliant-labs/reliant/internal/handlers/repo"
)

// The repo ownership check keeps hiding existence — a repo in someone else's
// project is "repo not found", exactly like a missing one — and a failure to
// look either up is no longer "not found" but the failure it is.
func TestRepoOwnership_ErrorClasses(t *testing.T) {
	t.Parallel()
	remove := func(t *testing.T, store *repo.MockRepository) error {
		svc := repo.NewTestRepo(t, repo.WithRepoDeps(repo.Deps{Repo: store}))
		_, err := svc.RemoveRepo(repo.AuthedContext(t), connect.NewRequest(&pb.RemoveRepoRequest{RepoId: "r-1"}))
		return err
	}
	foreign := ownedProjectRepo(t)
	foreign.GetProjectWithUserCheckFunc = func(context.Context, string, string) (*db.Project, error) {
		return nil, core.ErrProjectNotFound
	}
	err := remove(t, foreign)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.Equal(t, "repo not found", svcerr.ToConnect(err).Message(), "nothing says the project exists")

	for _, tc := range []struct {
		name string
		err  error
		code connect.Code
	}{
		{"a deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
		{"a cancellation", fmt.Errorf("query: %w", context.Canceled), connect.CodeCanceled},
		{"an opaque failure", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		for what, store := range map[string]*repo.MockRepository{
			"reading the repo":    {GetRepoFunc: func(context.Context, string) (*core.Repo, error) { return nil, tc.err }},
			"reading its project": withProjectErr(ownedProjectRepo(t), tc.err),
		} {
			t.Run(what+": "+tc.name, func(t *testing.T) {
				err := remove(t, store)
				require.Error(t, err)
				assert.Equal(t, tc.code, svcerr.Code(err), "err = %v", err)
				assert.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
			})
		}
	}
}

func withProjectErr(store *repo.MockRepository, err error) *repo.MockRepository {
	store.GetProjectWithUserCheckFunc = func(context.Context, string, string) (*db.Project, error) { return nil, err }
	return store
}
