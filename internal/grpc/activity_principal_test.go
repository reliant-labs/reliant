// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
)

func TestPrincipalReadsReliantsAuthContext(t *testing.T) {
	ctx := auth.WithUser(context.Background(), "user-1", "user", "ada@example.com")
	assert.Equal(t, activity.User("user-1", "ada@example.com"), Principal{}.Actor(ctx))
	assert.Equal(t, "user-1", Principal{}.Scope(ctx), "the scope is the user: reliant has no orgs")

	noEmail := auth.WithUser(context.Background(), "user-2", "user", "")
	assert.Equal(t, activity.User("user-2", "user-2"), Principal{}.Actor(noEmail))

	assert.Equal(t, activity.System("anonymous"), Principal{}.Actor(context.Background()))
	assert.Empty(t, Principal{}.Scope(context.Background()))
}

// createSettingOK answers CreateSetting, an rpc whose CRUD name implies an
// activity event, so the chain's Activity slot writes one when it succeeds.
type createSettingOK struct {
	reliantv1connect.UnimplementedSettingsServiceHandler
}

func (createSettingOK) CreateSetting(context.Context, *connect.Request[reliantv1.CreateSettingRequest]) (*connect.Response[reliantv1.CreateSettingResponse], error) {
	return connect.NewResponse(&reliantv1.CreateSettingResponse{}), nil
}

// The API listener's chain records the authenticated user, not
// system:anonymous: forge's default activity principal reads forge claims,
// which reliant's auth never stashes (it uses auth.WithUser).
func TestUserChainActivityEventNamesTheRealUser(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	asUser := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(auth.WithUser(ctx, "user-42", "user", "grace@example.com"), req)
		}
	})
	chain, err := NewUserChain(UserChainDeps{Auth: asUser, Logger: logger})
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewSettingsServiceHandler(createSettingOK{}, chain.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := reliantv1connect.NewSettingsServiceClient(srv.Client(), srv.URL)
	_, err = client.CreateSetting(context.Background(), connect.NewRequest(&reliantv1.CreateSettingRequest{}))
	require.NoError(t, err)

	var event map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "activity.event" {
			event = rec
		}
	}
	require.NotNil(t, event, "no activity.event line was logged:\n%s", logs.String())
	assert.Equal(t, "user-42", event["actor_id"])
	assert.Equal(t, "user", event["actor_kind"])
	assert.Equal(t, "user-42", event["scope"])
	assert.Equal(t, reliantv1connect.SettingsServiceCreateSettingProcedure, event["procedure"])
}
