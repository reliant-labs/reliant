package transport

import (
	"net/http"
	"testing"

	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
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
	_, ok := client.Transport.(daemonAuthRoundTripper)
	require.True(t, ok)
}
