package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// The wire contract with control-plane's TestsService.HasCoverageBlob: the
// path, the CALLER's bearer (never the internal-service identity), and the
// JSON field names.
func TestHasCoverageBlob_SpeaksControlPlanesWireContract(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = nil
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"present":true}`))
	}))
	defer srv.Close()

	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("never-sent")})
	got, err := c.HasCoverageBlob(context.Background(), "viewer-jwt", "org-1", "shop", "abc123")
	require.NoError(t, err)
	assert.Equal(t, CoverageBlob{Present: true}, got)
	assert.Equal(t, "/controlplane.v1.TestsService/HasCoverageBlob", gotPath)
	assert.Equal(t, "Bearer viewer-jwt", gotAuth)
	assert.Equal(t, map[string]string{"project": "shop", "blobId": "abc123", "orgId": "org-1"}, gotBody)

	_, err = c.HasCoverageBlob(context.Background(), "viewer-jwt", "", "shop", "abc123")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"project": "shop", "blobId": "abc123"}, gotBody, "no org: the caller's default")
}

func TestHasCoverageBlob_ReadsUncommitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"present":true,"uncommitted":true}`))
	}))
	defer srv.Close()
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}})
	got, err := c.HasCoverageBlob(context.Background(), "t", "", "shop", "abc")
	require.NoError(t, err)
	assert.Equal(t, CoverageBlob{Present: true, Uncommitted: true}, got)
}

// A control plane without the rpc (a bare 404 from the mux, or Unimplemented)
// is ErrCoverageBlobUnsupported; a refusal keeps its Connect code.
func TestHasCoverageBlob_Errors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		"no route": {http.StatusNotFound, "404 page not found", func(t *testing.T, err error) {
			assert.ErrorIs(t, err, ErrCoverageBlobUnsupported)
		}},
		"unimplemented": {http.StatusNotImplemented, `{"code":"unimplemented","message":"not served"}`, func(t *testing.T, err error) {
			assert.ErrorIs(t, err, ErrCoverageBlobUnsupported)
		}},
		"no tests:read": {http.StatusForbidden, `{"code":"permission_denied","message":"needs tests:read"}`, func(t *testing.T, err error) {
			assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			assert.Contains(t, err.Error(), "needs tests:read")
		}},
		"another org's project": {http.StatusNotFound, `{"code":"not_found","message":"no such project"}`, func(t *testing.T, err error) {
			assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			assert.False(t, errors.Is(err, ErrCoverageBlobUnsupported))
		}},
		"a proxy's 502": {http.StatusBadGateway, "bad gateway", func(t *testing.T, err error) {
			assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}})
			_, err := c.HasCoverageBlob(context.Background(), "t", "", "shop", "abc")
			require.Error(t, err)
			tc.check(t, err)
		})
	}
}
