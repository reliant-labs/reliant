package transport

import (
	"net/http"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/version"
	"github.com/stretchr/testify/require"
)

type captureRoundTripper struct {
	request *http.Request
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.request = req
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
}

func TestDaemonAuthRoundTripperInjectsAuthorizationHeader(t *testing.T) {
	capture := &captureRoundTripper{}
	rt := daemonAuthRoundTripper{next: capture, authToken: "daemon-token"}

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/test", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)
	require.NotNil(t, capture.request)
	require.Equal(t, "Bearer daemon-token", capture.request.Header.Get(daemonAuthorizationHeader))
}

func TestDaemonAuthRoundTripperPreservesExistingAuthorizationHeader(t *testing.T) {
	capture := &captureRoundTripper{}
	rt := daemonAuthRoundTripper{next: capture, authToken: "daemon-token"}

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/test", nil)
	require.NoError(t, err)
	req.Header.Set(daemonAuthorizationHeader, "Bearer pre-set")

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)
	require.NotNil(t, capture.request)
	require.Equal(t, "Bearer pre-set", capture.request.Header.Get(daemonAuthorizationHeader))
}

func TestNewDaemonHTTPClientRequiresAuthToken(t *testing.T) {
	_, _, err := NewDaemonHTTPClient(bootstrap.DaemonBootstrapConfig{
		AuthToken: "",
		GRPCURL:   "http://127.0.0.1:9190",
		TLSMode:   bootstrap.TLSModeH2C,
	})
	require.Error(t, err)
}

func TestNewDaemonHTTPClientWrapsTransportWithAuth(t *testing.T) {
	client, _, err := NewDaemonHTTPClient(bootstrap.DaemonBootstrapConfig{
		AuthToken: "daemon-token",
		GRPCURL:   "http://127.0.0.1:9190",
		TLSMode:   bootstrap.TLSModeH2C,
	})
	require.NoError(t, err)
	require.NotNil(t, client)
	rt, ok := client.Transport.(daemonAuthRoundTripper)
	require.True(t, ok)
	require.Equal(t, ClientIdentity(DaemonClientKind), rt.client, "the gateway client names the daemon on every request")
}

// Every request names the daemon: a server may refuse one with no
// Reliant-Client header, the gateway stream's open included.
func TestDaemonAuthRoundTripperSetsReliantClient(t *testing.T) {
	capture := &captureRoundTripper{}
	rt := daemonAuthRoundTripper{next: capture, authToken: "daemon-token", client: ClientIdentity(DaemonClientKind)}

	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/reliant.v1.ToolsDaemonService/ConnectDaemon", nil)
	require.NoError(t, err)
	req.Header.Set(daemonAuthorizationHeader, "Bearer pre-set")

	_, err = rt.RoundTrip(req)
	require.NoError(t, err)
	want := "daemon/" + strings.TrimPrefix(version.Get().Version, "v")
	require.Equal(t, want, capture.request.Header.Get(ReliantClientHeader))
	require.Empty(t, req.Header.Get(ReliantClientHeader), "the caller's request is not mutated")
}
