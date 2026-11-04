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
	"sync"
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

// updatePresetOK answers UpdatePreset as a successful update of preset-7,
// an rpc that declares an "update preset" activity event, so the chain's
// Activity slot writes one when it succeeds.
type updatePresetOK struct {
	reliantv1connect.UnimplementedPresetServiceHandler
}

func (updatePresetOK) UpdatePreset(context.Context, *connect.Request[reliantv1.UpdatePresetRequest]) (*connect.Response[reliantv1.UpdatePresetResponse], error) {
	return connect.NewResponse(&reliantv1.UpdatePresetResponse{
		Success: true,
		Preset:  &reliantv1.PresetInfo{Id: "preset-7", Name: "careful"},
	}), nil
}

// activityEvents serves a PresetService through the API listener's real
// chain, authenticated as user-42, and returns a client for it plus every
// activity.event record the chain logged.
func activityEvents(t *testing.T, svc reliantv1connect.PresetServiceHandler) (reliantv1connect.PresetServiceClient, func() []map[string]any) {
	t.Helper()
	var (
		mu   sync.Mutex
		logs bytes.Buffer
	)
	logger := slog.New(slog.NewJSONHandler(lockedWriter{mu: &mu, w: &logs}, &slog.HandlerOptions{Level: slog.LevelInfo}))

	asUser := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(auth.WithUser(ctx, "user-42", "user", "grace@example.com"), req)
		}
	})
	chain, err := NewUserChain(UserChainDeps{Auth: asUser, Logger: logger})
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewPresetServiceHandler(svc, chain.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return reliantv1connect.NewPresetServiceClient(srv.Client(), srv.URL), func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		var events []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var rec map[string]any
			if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "activity.event" {
				events = append(events, rec)
			}
		}
		return events
	}
}

// lockedWriter serializes the handler's writes with the test's reads.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// The API listener's chain records the authenticated user, not
// system:anonymous: forge's default activity principal reads forge claims,
// which reliant's auth never stashes (it uses auth.WithUser). UpdatePreset is
// the witness: an authenticated update writes one event naming the user as
// actor and scope, and the preset the response names as its target.
func TestUserChainActivityEventNamesTheRealUser(t *testing.T) {
	client, events := activityEvents(t, updatePresetOK{})

	_, err := client.UpdatePreset(context.Background(), connect.NewRequest(&reliantv1.UpdatePresetRequest{ProjectId: "p-1", Name: "careful"}))
	require.NoError(t, err)

	got := events()
	require.Len(t, got, 1, "UpdatePreset must log exactly one activity.event")
	event := got[0]
	assert.Equal(t, "user", event["actor_kind"])
	assert.Equal(t, "user-42", event["actor_id"])
	assert.Equal(t, "user-42", event["scope"])
	assert.Equal(t, "update", event["verb"])
	assert.Equal(t, "preset", event["target_kind"])
	assert.Equal(t, "preset-7", event["target_id"])
	assert.Equal(t, reliantv1connect.PresetServiceUpdatePresetProcedure, event["procedure"])
}
