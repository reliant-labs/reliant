package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The wire contract with control-plane's AutopilotReceiverInternalService
// (pinned on that side by internal/handlers/autopilot_receiver_internal's
// TestWireJSON_MatchesReliantClient): the paths, the internal-service bearer,
// and the JSON field names.
func TestAutopilotReceiver_SpeaksControlPlanesWireContract(t *testing.T) {
	type call struct {
		path, auth string
		body       map[string]any
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		calls = append(calls, call{r.URL.Path, r.Header.Get("Authorization"), body})
		switch r.URL.Path {
		case autopilotListPath:
			if body["pageToken"] == "" {
				_, _ = w.Write([]byte(`{"receivers":[{"activationId":"a","orgId":"o"},{"activationId":"b"}],"nextPageToken":"b"}`))
				return
			}
			_, _ = w.Write([]byte(`{"receivers":[{"activationId":"c"}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c := New(Deps{BaseURL: srv.URL, Sign: signWith("s")})
	ctx := context.Background()

	require.NoError(t, c.UpsertAutopilotReceiver(ctx, AutopilotReceiver{
		ActivationID: "t-1", UserID: "u-1", URL: "https://r.test/hooks/t-1", Token: "whk_x", Repos: []string{"github.com/acme/shop"},
	}))
	require.NoError(t, c.DeleteAutopilotReceiver(ctx, "t-1"))
	ids, err := c.ListAutopilotReceivers(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, ids, "every page")

	require.Len(t, calls, 4)
	up := calls[0]
	assert.Equal(t, "/controlplane.v1.AutopilotReceiverInternalService/UpsertAutopilotReceiver", up.path)
	assert.Equal(t, "Bearer signed-s", up.auth)
	keys := make([]string, 0, len(up.body))
	for k := range up.body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"activationId", "scope", "token", "url", "userId"}, keys)
	assert.Equal(t, map[string]any{"projects": []any{}, "repos": []any{"github.com/acme/shop"}}, up.body["scope"])
	assert.Equal(t, "/controlplane.v1.AutopilotReceiverInternalService/DeleteAutopilotReceiver", calls[1].path)
	assert.Equal(t, map[string]any{"activationId": "t-1"}, calls[1].body)
	assert.Equal(t, "/controlplane.v1.AutopilotReceiverInternalService/ListAutopilotReceivers", calls[2].path)
	assert.Equal(t, "b", calls[3].body["pageToken"])
}

func TestAutopilotReceiver_RefusalsAreTyped(t *testing.T) {
	for name, tc := range map[string]struct {
		handler     http.HandlerFunc
		unsupported bool
		reason      string
	}{
		"no route": {handler: http.NotFound, unsupported: true},
		"unimplemented": {handler: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"code":"unimplemented"}`))
		}, unsupported: true},
		"no org": {handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Forge-Error-Reason", ReasonAutopilotNoOrg)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"failed_precondition","message":"no organization"}`))
		}, reason: ReasonAutopilotNoOrg},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			err := New(Deps{BaseURL: srv.URL, Sign: signWith("s")}).UpsertAutopilotReceiver(context.Background(), AutopilotReceiver{ActivationID: "t"})
			if tc.unsupported {
				assert.ErrorIs(t, err, ErrAutopilotUnsupported)
				return
			}
			var refused *AutopilotError
			require.ErrorAs(t, err, &refused)
			assert.Equal(t, tc.reason, refused.Reason)
			assert.Equal(t, "no organization", err.Error())
		})
	}
}

// Without an internal-service identity nothing goes over the wire.
func TestAutopilotReceiver_WithoutAnIdentityIsUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an unsigned call reached the control plane")
	}))
	defer srv.Close()
	_, err := New(Deps{BaseURL: srv.URL}).ListAutopilotReceivers(context.Background())
	assert.ErrorIs(t, err, ErrAutopilotUnsupported)
}
