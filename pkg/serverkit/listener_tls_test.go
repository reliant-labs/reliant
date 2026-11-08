package serverkit_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// writeSelfSigned writes a self-signed certificate for 127.0.0.1 and its key
// into dir, returning both paths and the certificate.
func writeSelfSigned(t *testing.T, dir string) (certPath, keyPath string, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath, cert
}

func tlsListener(t *testing.T, certPath, keyPath string) *serverkit.Listener {
	t.Helper()
	cfg := loopback()
	cfg.TLSCertPath, cfg.TLSKeyPath = certPath, keyPath
	l, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: cfg,
		Chain:  chainFor(t, serverkit.AudienceUser, "t"),
		Routes: pingRoutes(),
		Front: map[string]http.Handler{"/healthz": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		})},
	})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	return l
}

// A certificate that cannot be loaded fails Listen — boot — before anything
// binds, so the listener never reports Ready and a supervisor never moves on
// to the next node believing it is serving. It used to load in Run's
// ServeTLS: Listen bound, Ready said nil, and only then did Run fail.
func TestListener_BadTLSCertificateFailsListenNotRun(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, _ := writeSelfSigned(t, dir)
	garbage := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherDir := t.TempDir()
	_, otherKey, _ := writeSelfSigned(t, otherDir)

	for name, paths := range map[string][2]string{
		"missing certificate": {filepath.Join(dir, "absent.crt"), keyPath},
		"missing key":         {certPath, filepath.Join(dir, "absent.key")},
		"not PEM":             {garbage, keyPath},
		"mismatched key":      {certPath, otherKey},
	} {
		t.Run(name, func(t *testing.T) {
			l := tlsListener(t, paths[0], paths[1])
			err := l.Listen(context.Background())
			if err == nil {
				_ = l.Close(context.Background())
				t.Fatal("Listen succeeded with a certificate that cannot be served")
			}
			if !strings.Contains(err.Error(), "TLS certificate") {
				t.Errorf("Listen error %q does not say the certificate is the problem", err)
			}
			if l.Addr() != nil {
				t.Errorf("the listener bound %v despite the bad certificate", l.Addr())
			}
			if l.Ready(context.Background()) == nil {
				t.Error("the listener reports Ready after Listen failed")
			}
		})
	}
}

// A good pair is loaded once by Listen and served by Run, over HTTP/2.
func TestListener_ServesTLSWithTheCertificateListenLoaded(t *testing.T) {
	certPath, keyPath, cert := writeSelfSigned(t, t.TempDir())
	l := tlsListener(t, certPath, keyPath)
	if err := l.Listen(context.Background()); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	pool := x509.NewCertPool()
	pool.AddCert(cert)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}}
	resp, err := client.Get("https://" + l.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET over TLS: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("GET = %d %q", resp.StatusCode, body)
	}
	if resp.ProtoMajor != 2 {
		t.Errorf("served %s, want HTTP/2 over TLS", resp.Proto)
	}
}
