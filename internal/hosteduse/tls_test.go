package hosteduse

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivateControllerTLSVerifiesTrustAndName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	transport, err := privateControllerTransport(ca)
	require.NoError(t, err)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, 204, resp.StatusCode)
	// A changed projected trust file must be re-read; a cached success must not
	// accept the old leaf under a now-invalid CA bundle.
	transport.CloseIdleConnections()
	require.NoError(t, os.WriteFile(ca, []byte("invalid"), 0600))
	_, err = client.Get(server.URL)
	require.ErrorContains(t, err, "contains no certificates")
	// System roots do not authenticate this private issuer.
	_, err = http.Get(server.URL)
	require.Error(t, err)
	// Exercise the actual client handshake with a trusted issuer but a wrong
	// DNS identity, keeping the dial local without bypassing verification.
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	roots, err := controllerRoots(ca)
	require.NoError(t, err)
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	raw, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", u.Host)
	require.NoError(t, err)
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{RootCAs: roots, ServerName: "workspace-controller.wrong.svc.cluster.local", MinVersion: tls.VersionTLS12})
	err = conn.HandshakeContext(context.Background())
	require.ErrorContains(t, err, "certificate")
}
