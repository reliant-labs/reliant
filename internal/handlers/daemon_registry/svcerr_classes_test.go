// Copyright (c) 2025 Reliant Labs
package daemon_registry_test

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
	daemon_registry "github.com/reliant-labs/reliant/internal/handlers/daemon_registry"
)

// How a daemon lookup can fail, and the code each reaches a client with. A
// miss the store classified stays NotFound; a database outage is Internal
// rather than "daemon not found", and its text never reaches the wire.
func TestDaemonRegistry_StoreFailuresOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
	}{
		{"store not found", svcerr.NotFound("daemon"), connect.CodeNotFound},
		{"deadline", fmt.Errorf("failed to get daemon d1: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
		{"canceled", fmt.Errorf("failed to get daemon d1: %w", context.Canceled), connect.CodeCanceled},
		{"opaque", errors.New("dial tcp: dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &daemon_registry.MockRepository{
				GetDaemonFunc:           func(context.Context, string) (*db.Daemon, error) { return nil, tc.err },
				ListDaemonsByUserIDFunc: func(context.Context, string) ([]*db.Daemon, error) { return nil, tc.err },
			}
			_, client := daemon_registry.NewTestDaemonRegistryServer(t, daemon_registry.WithDaemonRegistryDeps(daemon_registry.Deps{Repo: repo}))

			_, err := client.GetDaemon(context.Background(), connect.NewRequest(&pb.GetDaemonRequest{DaemonId: "d1"}))
			assertWire(t, err, tc.want)
			_, err = client.ResumeDaemon(context.Background(), connect.NewRequest(&pb.ResumeDaemonRequest{DaemonId: "d1"}))
			assertWire(t, err, tc.want)
			_, err = client.RefreshLocalModels(context.Background(), connect.NewRequest(&pb.RefreshLocalModelsRequest{DaemonId: "d1"}))
			assertWire(t, err, tc.want)
			if tc.want != connect.CodeNotFound {
				_, err = client.ListDaemons(context.Background(), connect.NewRequest(&pb.ListDaemonsRequest{}))
				assertWire(t, err, tc.want)
			}
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
