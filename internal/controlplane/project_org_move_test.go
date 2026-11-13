package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// The wire contract with control-plane's ProjectOrgMoveInternalService
// (proto/services/project_org_move_internal/v1 there): the paths, the
// internal-service bearer, and the protojson field names.
func TestProjectOrgMove_SpeaksControlPlanesWireContract(t *testing.T) {
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
		if r.URL.Path == projectOrgMovePreparePath {
			_, _ = w.Write([]byte(`{"revokedSecretAccess":2,"revokedHostedHttpGrants":1}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})
	ctx := context.Background()
	step := ProjectOrgMoveStep{MoveID: "m-1", ProjectID: "p-1", FromOrgID: "acme", ToOrgID: "globex"}

	revoked, err := c.PrepareProjectOrgMove(ctx, step, "u-1")
	require.NoError(t, err)
	assert.Equal(t, 3, revoked, "secret access and legacy grants together")
	require.NoError(t, c.AbortProjectOrgMove(ctx, step))
	require.NoError(t, c.RecordProjectOrgMove(ctx, step, "shop", map[string]int64{"chats": 3}))

	require.Len(t, calls, 3)
	want := map[string]any{"moveId": "m-1", "projectId": "p-1", "fromOrgId": "acme", "toOrgId": "globex"}
	assert.Equal(t, "/controlplane.v1.ProjectOrgMoveInternalService/PrepareProjectOrgMove", calls[0].path)
	assert.Equal(t, "Bearer signed-s", calls[0].auth)
	prepare := map[string]any{"actorUserId": "u-1"}
	for k, v := range want {
		prepare[k] = v
	}
	assert.Equal(t, prepare, calls[0].body)
	assert.Equal(t, "/controlplane.v1.ProjectOrgMoveInternalService/AbortProjectOrgMove", calls[1].path)
	assert.Equal(t, want, calls[1].body)
	assert.Equal(t, "/controlplane.v1.ProjectOrgMoveInternalService/RecordProjectOrgMove", calls[2].path)
	assert.Equal(t, "shop", calls[2].body["projectName"])
	assert.Equal(t, map[string]any{"chats": "3"}, calls[2].body["counts"])
}

// A refusal carries its code and reason, and says whether a retry can help;
// a control plane without the service is ErrProjectOrgMoveUnsupported.
func TestProjectOrgMove_RefusalsAreTyped(t *testing.T) {
	step := ProjectOrgMoveStep{MoveID: "m", ProjectID: "p", FromOrgID: "a", ToOrgID: "b"}
	refuse := func(status int, code, reason string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			if reason != "" {
				w.Header().Set("X-Forge-Error-Reason", reason)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":"` + code + `","message":"no"}`))
		}
	}
	for name, tc := range map[string]struct {
		handler     http.HandlerFunc
		unsupported bool
		permanent   bool
		reason      string
	}{
		"no route":      {handler: http.NotFound, unsupported: true},
		"unimplemented": {handler: refuse(http.StatusNotImplemented, "unimplemented", ""), unsupported: true},
		"not an admin":  {handler: refuse(http.StatusForbidden, "permission_denied", ReasonMoveSourceAdminRequired), permanent: true, reason: ReasonMoveSourceAdminRequired},
		"aborted":       {handler: refuse(http.StatusBadRequest, "failed_precondition", ReasonMoveAborted), permanent: true, reason: ReasonMoveAborted},
		"an outage":     {handler: refuse(http.StatusServiceUnavailable, "unavailable", "")},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})
			_, err := c.PrepareProjectOrgMove(context.Background(), step, "u")
			require.Error(t, err)
			if tc.unsupported {
				require.ErrorIs(t, err, ErrProjectOrgMoveUnsupported)
				return
			}
			var refusal *ProjectOrgMoveError
			require.True(t, errors.As(err, &refusal), "%v", err)
			assert.Equal(t, tc.permanent, refusal.Permanent())
			assert.Equal(t, tc.reason, refusal.Reason)
		})
	}

	// No identity to sign with is unsupported, not an outage.
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: "http://127.0.0.1:1"}})
	require.ErrorIs(t, c.AbortProjectOrgMove(context.Background(), step), ErrProjectOrgMoveUnsupported)
}
