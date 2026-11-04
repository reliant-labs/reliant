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

// privateControllerTransport uses only the dedicated trust bundle and normal
// hostname verification. A new handshake re-reads projected cert-manager files.
// It never uses a proxy for the private controller or disables verification.
func privateControllerTransport(caFile string) (*http.Transport, error) {
	if _, err := controllerRoots(caFile); err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		roots, err := controllerRoots(caFile)
		if err != nil {
			return nil, err
		}
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
		return dialer.DialContext(ctx, network, addr)
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
