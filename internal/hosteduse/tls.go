package hosteduse

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"time"
)

// privateControllerTransport is the transport for the private controller hop,
// built from scratch rather than cloned from http.DefaultTransport so no
// process-wide setting reaches it. It never uses a proxy: the controller is a
// cluster-internal endpoint, so Proxy is nil and HTTP(S)_PROXY / NO_PROXY in
// the worker's environment are never read. With a caFile it trusts only that
// bundle, with normal hostname verification, and a new handshake re-reads the
// projected cert-manager files; without one it uses the system roots. It never
// disables verification.
func privateControllerTransport(caFile string) (*http.Transport, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if caFile == "" {
		return tr, nil
	}
	if _, err := controllerRoots(caFile); err != nil {
		return nil, err
	}
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		roots, err := controllerRoots(caFile)
		if err != nil {
			return nil, err
		}
		tlsDialer := tls.Dialer{NetDialer: dialer, Config: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
		return tlsDialer.DialContext(ctx, network, addr)
	}
	return tr, nil
}

func controllerRoots(file string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(file)
	if err != nil {
		return nil, errors.New("hosted action controller CA bundle cannot be read")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("hosted action controller CA bundle contains no certificates")
	}
	return roots, nil
}
