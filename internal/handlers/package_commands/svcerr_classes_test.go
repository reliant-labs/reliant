// Copyright (c) 2025 Reliant Labs
package package_commands_test

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
	package_commands "github.com/reliant-labs/reliant/internal/handlers/package_commands"
)

// How a store read can fail, and the code each reaches a client with, over a
// real Connect client and the svcerr interceptor. A miss the store classified
// keeps its code; a database outage is Internal with the rpc's own words, and
// its text never reaches the wire.
func TestPackageCommands_StoreFailuresOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
	}{
		{"store not found", svcerr.NotFound("project"), connect.CodeNotFound},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
		{"canceled", fmt.Errorf("query: %w", context.Canceled), connect.CodeCanceled},
		{"opaque", errors.New("dial tcp: dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &package_commands.StubRepo{
				GetProjectWithUserCheckFunc: func(context.Context, string, string) (*db.Project, error) { return nil, tc.err },
				GetWorktreeFunc:             func(context.Context, string) (*db.Worktree, error) { return nil, tc.err },
			}
			_, client := package_commands.NewTestPackageCommandsServer(t, package_commands.WithPackageCommandsDeps(package_commands.Deps{Repo: repo}))
			_, err := client.GetCommandFavorites(context.Background(), connect.NewRequest(&pb.GetCommandFavoritesRequest{ProjectId: "p1"}))
			assertWire(t, err, tc.want)
			_, err = client.SetCommandFavorite(context.Background(), connect.NewRequest(&pb.SetCommandFavoriteRequest{ProjectId: "p1", CommandKey: "npm:test", IsFavorite: true}))
			assertWire(t, err, tc.want)
			worktreeID := "w1"
			_, err = client.RunCommand(context.Background(), connect.NewRequest(&pb.RunPackageCommandRequest{
				WorktreeId: &worktreeID, CommandName: "test", PackageType: pb.PackageType_PACKAGE_TYPE_NPM,
			}))
			assertWire(t, err, tc.want)
		})
	}
}

func assertWire(t *testing.T, err error, want connect.Code) {
	t.Helper()
	var cerr *connect.Error
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, want, cerr.Code(), "message: %s", cerr.Message())
	assert.NotContains(t, cerr.Message(), "secret", "an opaque error's text never reaches the client")
}
