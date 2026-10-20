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

	"github.com/reliant-labs/reliant/internal/toolexec"
)

func signWith(secret string) (string, error) { return "signed-" + secret, nil }

// The wire contract with control-plane's DaemonInternalService (pinned on
// that side by internal/handlers/daemon_internal's TestWireJSON_MatchesReliantClient):
// the path, the internal-service bearer, and the JSON field names.
func TestRunResumeClient_SpeaksControlPlanesWireContract(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"resumed":true}`))
	}))
	defer srv.Close()

	c := NewRunResumeClient(Endpoint{URL: srv.URL + "/", InternalServiceSecret: "s3"}, signWith)
	require.NotNil(t, c)
	resumed, err := c.ResumeDaemonForRun(context.Background(), "ext-user", "daemon-1")
	require.NoError(t, err)
	assert.True(t, resumed)
	assert.Equal(t, "/controlplane.v1.DaemonInternalService/ResumeDaemonForRun", gotPath)
	assert.Equal(t, "Bearer signed-s3", gotAuth)
	assert.Equal(t, map[string]string{"userId": "ext-user", "daemonId": "daemon-1"}, gotBody)
}

// A control plane that predates the door says so as Unimplemented (or a bare
// 404 from the mux); the router then falls back to the owner's token.
func TestRunResumeClient_AnOlderControlPlaneIsUnsupported(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"no route": http.NotFound,
		"unimplemented": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"code":"unimplemented","message":"not served"}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			c := NewRunResumeClient(Endpoint{URL: srv.URL, InternalServiceSecret: "s"}, signWith)
			_, err := c.ResumeDaemonForRun(context.Background(), "u", "d")
			assert.ErrorIs(t, err, toolexec.ErrRunResumeUnsupported)
		})
	}
}

// A refusal carries control-plane's own words, and is not mistaken for an
// older control plane.
func TestRunResumeClient_ARefusalCarriesTheReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"failed_precondition","message":"the machine failed to start; only its owner can retry it"}`))
	}))
	defer srv.Close()
	c := NewRunResumeClient(Endpoint{URL: srv.URL, InternalServiceSecret: "s"}, signWith)
	_, err := c.ResumeDaemonForRun(context.Background(), "u", "d")
	require.Error(t, err)
	assert.False(t, errors.Is(err, toolexec.ErrRunResumeUnsupported))
	var refusal *RunResumeError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "failed_precondition", refusal.Code)
	assert.Contains(t, err.Error(), "only its owner can retry it")
}

// Without a URL or a secret there is no door to call.
func TestNewRunResumeClient_NeedsAURLAndASecret(t *testing.T) {
	assert.Nil(t, NewRunResumeClient(Endpoint{}, signWith))
	assert.Nil(t, NewRunResumeClient(Endpoint{URL: "http://cp"}, signWith))
	assert.Nil(t, NewRunResumeClient(Endpoint{InternalServiceSecret: "s"}, signWith))
}
