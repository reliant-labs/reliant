// Copyright (c) 2025 Reliant Labs
package settings

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
)

// storeFailures is how a repository read can fail, and the code each one
// reaches a client with. A failure the store classified keeps its code; an
// opaque one is Internal, and its text — which can carry a DSN — never reaches
// the wire.
var storeFailures = []struct {
	name string
	err  error
	want connect.Code
}{
	{"store not found", fmt.Errorf("%w: theme", svcerr.NotFound("setting")), connect.CodeNotFound},
	{"deadline", fmt.Errorf("query settings: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
	{"canceled", fmt.Errorf("query settings: %w", context.Canceled), connect.CodeCanceled},
	{"opaque", errors.New("dial tcp: dsn=postgres://u:secret@h"), connect.CodeInternal},
}

// The settings rpcs read and write through the store: its classified errors
// pass, anything else is Internal with the rpc's own words.
func TestSettings_StoreFailuresOnTheWire(t *testing.T) {
	for _, tc := range storeFailures {
		t.Run(tc.name, func(t *testing.T) {
			repo := &MockRepository{
				ListSettingsFunc: func(context.Context, string, *string) ([]*db.Setting, error) { return nil, tc.err },
				GetSettingFunc:   func(context.Context, string, *string, string) (*db.Setting, error) { return nil, tc.err },
			}
			_, client := NewTestSettingsServer(t, WithSettingsDeps(Deps{Repo: repo}), WithPlaceholderDeps())

			_, err := client.ListSettings(context.Background(), connect.NewRequest(&reliantv1.ListSettingsRequest{}))
			assertWire(t, err, tc.want)

			value := "dark"
			_, err = client.UpdateSetting(context.Background(), connect.NewRequest(&reliantv1.UpdateSettingRequest{Key: "theme", Value: &value}))
			assertWire(t, err, tc.want)
		})
	}
}

// Control-plane is a remote Connect client: its error is the logged cause,
// never the verdict. The client reads this rpc's sentence and code whatever
// control-plane said.
func TestSettings_MintReliantKey_ControlPlaneErrorIsTheCause(t *testing.T) {
	remote := connect.NewError(connect.CodeNotFound, errors.New("billing-svc.internal:8443: no wallet row for user"))
	cp := &StubControlPlane{MintLLMKeyFunc: func(context.Context, string, string) (controlplane.LLMKey, error) {
		return controlplane.LLMKey{}, remote
	}}
	svc := NewTestSettings(t, WithSettingsDeps(Deps{ControlPlane: cp}), WithPlaceholderDeps())
	const userID = "svcerr-mint-user"
	auth.SetUserJWT(userID, "test-jwt")
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	_, err := svc.SyncReliantProvider(ctx, connect.NewRequest(&reliantv1.SyncReliantProviderRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, svcerr.Code(err), "control-plane's NotFound is not this rpc's")
	msg := svcerr.ToConnect(err).Message()
	assert.Equal(t, "failed to mint Reliant LLM key", msg)
	assert.NotContains(t, msg, "billing-svc")
	assert.Same(t, remote, svcerr.Cause(err), "the remote error is the logged cause")
}

func assertWire(t *testing.T, err error, want connect.Code) {
	t.Helper()
	var cerr *connect.Error
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, want, cerr.Code(), "message: %s", cerr.Message())
	assert.NotContains(t, cerr.Message(), "secret", "an opaque error's text never reaches the client")
}
