package kubedial

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func externalVerifiedRoute() External {
	return External{Namespace: "prod", Service: "pgbouncer", Database: "ledger", Username: "ledger_ro", PasswordSecret: "reader", SSLMode: SSLModeVerifyFull, CASecret: "ca", CAKey: "ca.crt"}
}
func externalPassword() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "reader"}, Data: map[string][]byte{"password": []byte("private-password")}}
}
func externalCA(pem []byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "ca"}, Data: map[string][]byte{"ca.crt": pem}}
}

// Loopback TLS handshakes exercise chain and SAN verification without a
// cluster or PostgreSQL server. TCP buffering lets a rejection alert and the
// server's certificate flight cross without net.Pipe's simultaneous-write
// deadlock. Go's suite timeout bounds a stuck handshake, not a success budget.
func externalHandshake(t *testing.T, cfg *tls.Config, cert tls.Certificate) error {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		externalLoopbackError(t, err)
	}
	defer listener.Close()
	b, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		externalLoopbackError(t, err)
	}
	defer b.Close()
	a, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	server := tls.Server(a, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	done := make(chan error, 1)
	go func() { done <- server.Handshake() }()
	client := tls.Client(b, cfg)
	err = client.Handshake()
	_ = a.Close()
	_ = b.Close()
	<-done
	return err
}
func externalLoopbackError(t *testing.T, err error) {
	t.Helper()
	for _, resource := range []error{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.EADDRNOTAVAIL, syscall.EACCES, syscall.EPERM} {
		if errors.Is(err, resource) {
			t.Skipf("loopback sockets unavailable on this host: %v", err)
		}
	}
	t.Fatal(err)
}

func TestExternalVerifiedTLS(t *testing.T) {
	t.Parallel()
	ca, other := newCA(t), newCA(t)
	for _, tc := range []struct {
		name       string
		trusted    []byte
		dns        string
		serverName string
		configmap  bool
		binary     bool
		ok         bool
	}{
		{name: "Secret CA and derived SAN", trusted: ca.caPEM, dns: "pgbouncer.prod.svc", ok: true},
		{name: "ConfigMap text CA and explicit SAN", trusted: ca.caPEM, dns: "pooler.example.test", serverName: "pooler.example.test", configmap: true, ok: true},
		{name: "ConfigMap binary CA", trusted: ca.caPEM, dns: "pgbouncer.prod.svc", configmap: true, binary: true, ok: true},
		{name: "wrong CA", trusted: other.caPEM, dns: "pgbouncer.prod.svc"},
		{name: "wrong hostname", trusted: ca.caPEM, dns: "other.prod.svc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := externalVerifiedRoute()
			x.TLSServerName = tc.serverName
			var trust runtime.Object = externalCA(tc.trusted)
			if tc.configmap {
				x.CASecret = ""
				x.CAConfigMap = "ca"
				cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "ca"}}
				if tc.binary {
					cm.BinaryData = map[string][]byte{"ca.crt": tc.trusted}
				} else {
					cm.Data = map[string]string{"ca.crt": string(tc.trusted)}
				}
				trust = cm
			}
			cs := fake.NewClientset(externalPassword(), trust)
			cfg, err := NewWithClientset(cs, nil).ExternalConnConfig(context.Background(), x)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.RootCAs == nil || len(cfg.Fallbacks) != 0 {
				t.Fatal("verification or no-fallback invariant lost")
			}
			err = externalHandshake(t, cfg.TLSConfig, ca.serverCert(t, tc.dns))
			if (err == nil) != tc.ok {
				t.Fatalf("success=%v err=%v", tc.ok, err)
			}
		})
	}
}

func TestExternalTrustRefusedBeforePassword(t *testing.T) {
	t.Parallel()
	ca := newCA(t)
	for _, tc := range []struct {
		name     string
		change   func(*External)
		trust    runtime.Object
		apiError bool
	}{
		{name: "no trust", change: func(x *External) { x.CASecret = "" }},
		{name: "both sources", change: func(x *External) { x.CAConfigMap = "ca" }},
		{name: "no key", change: func(x *External) { x.CAKey = "" }},
		{name: "bad name", change: func(x *External) { x.TLSServerName = "*.prod.svc" }},
		{name: "unknown mode", change: func(x *External) { x.SSLMode = "prefer" }},
		{name: "require mismatched", change: func(x *External) { x.SSLMode = SSLModeRequire }},
		{name: "disable mismatched", change: func(x *External) { x.SSLMode = SSLModeDisable }},
		{name: "missing resource"},
		{name: "missing key", trust: externalCA(nil)},
		{name: "bad PEM", trust: externalCA([]byte("private-sentinel"))},
		{name: "broken block before valid PEM", trust: externalCA(append([]byte("-----BEGIN CERTIFICATE-----\ninvalid\n"), ca.caPEM...))},
		{name: "partial bundle", trust: externalCA(append(append([]byte{}, ca.caPEM...), []byte("private-sentinel")...))},
		{name: "invalid certificate", trust: externalCA([]byte("-----BEGIN CERTIFICATE-----\nYQ==\n-----END CERTIFICATE-----"))},
		{name: "ambiguous ConfigMap", change: func(x *External) { x.CASecret = ""; x.CAConfigMap = "ca" }, trust: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "ca"}, Data: map[string]string{"ca.crt": string(ca.caPEM)}, BinaryData: map[string][]byte{"ca.crt": ca.caPEM}}},
		{name: "resource error sanitized", apiError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := externalVerifiedRoute()
			if tc.change != nil {
				tc.change(&x)
			}
			cs := fake.NewClientset(externalPassword())
			if tc.trust != nil {
				if err := cs.Tracker().Add(tc.trust); err != nil {
					t.Fatal(err)
				}
			}
			if tc.apiError {
				cs.PrependReactor("get", "secrets", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("private-sentinel") })
			}
			_, err := NewWithClientset(cs, nil).ExternalConnConfig(context.Background(), x)
			if err == nil {
				t.Fatal("accepted invalid trust")
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("error leaked material")
			}
			for _, a := range cs.Actions() {
				if get, ok := a.(ktesting.GetAction); ok && get.GetName() == "reader" {
					t.Fatal("password read before trust validation")
				}
			}
		})
	}
}

func TestExternalLegacyModesKeepValidationAndTLS(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", SSLModeRequire, SSLModeDisable} {
		t.Run(mode, func(t *testing.T) {
			// Public External historically accepts any nonempty route strings;
			// tightening new trust validation must not tighten legacy modes.
			x := External{Namespace: "legacy_ns", Service: "legacy_service", Database: "d", Username: "u", PasswordSecret: "legacy_secret", SSLMode: mode}
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: x.Namespace, Name: x.PasswordSecret}, Data: map[string][]byte{"password": []byte("pw")}}
			cs := fake.NewClientset(sec)
			cfg, err := NewWithClientset(cs, nil).ExternalConnConfig(context.Background(), x)
			if err != nil {
				t.Fatal(err)
			}
			if mode == SSLModeDisable {
				if cfg.TLSConfig != nil {
					t.Fatal("disable enabled TLS")
				}
			} else if cfg.TLSConfig == nil || !cfg.TLSConfig.InsecureSkipVerify {
				t.Fatal("legacy require behavior changed")
			}
			if len(cs.Actions()) != 1 {
				t.Fatal("legacy mode read trust")
			}
		})
	}
}

func TestExternalCAReReadAfterRotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first, second := newCA(t), newCA(t)
	cs := fake.NewClientset(externalPassword(), externalCA(first.caPEM))
	c := NewWithClientset(cs, nil)
	old, err := c.ExternalConnConfig(ctx, externalVerifiedRoute())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Secrets("prod").Update(ctx, externalCA(second.caPEM), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	current, err := c.ExternalConnConfig(ctx, externalVerifiedRoute())
	if err != nil {
		t.Fatal(err)
	}
	cert := second.serverCert(t, "pgbouncer.prod.svc")
	if externalHandshake(t, old.TLSConfig, cert) == nil {
		t.Fatal("old config silently mutated")
	}
	if err := externalHandshake(t, current.TLSConfig, cert); err != nil {
		t.Fatal("new invocation did not read rotated CA", err)
	}
}
