package kubedial

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	streamhttp "k8s.io/streaming/pkg/httpstream"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/dbquery"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

func ptr[T any](v T) *T { return &v }

// slice builds an EndpointSlice of service with one endpoint per pod; a pod
// named "!x" is a not-ready endpoint x.
func slice(ns, service string, port int32, pods ...string) *discoveryv1.EndpointSlice {
	s := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: service + "-abc", Namespace: ns, Labels: map[string]string{discoveryv1.LabelServiceName: service}},
		Ports:      []discoveryv1.EndpointPort{{Name: ptr("postgres"), Port: ptr(port)}},
	}
	for _, p := range pods {
		ready := true
		if strings.HasPrefix(p, "!") {
			p, ready = p[1:], false
		}
		s.Endpoints = append(s.Endpoints, discoveryv1.Endpoint{
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: p, Namespace: ns},
		})
	}
	return s
}

// fakePods is a PodDialer that records what it was asked for and serves each
// stream from a function.
type fakePods struct {
	mu    sync.Mutex
	calls []string
	serve func(ns, pod string, port int) (io.ReadWriteCloser, error)
}

func (f *fakePods) DialPod(_ context.Context, ns, pod string, port int) (io.ReadWriteCloser, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s/%s:%d", ns, pod, port))
	f.mu.Unlock()
	return f.serve(ns, pod, port)
}

func (f *fakePods) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// pipeStream returns the client end of an in-memory stream and the server end.
func pipeStream() (io.ReadWriteCloser, net.Conn) {
	c, s := net.Pipe()
	return c, s
}

func TestServiceName(t *testing.T) {
	if got := ServiceName("app", 1); got != "app-rw" {
		t.Errorf("one instance: %s", got)
	}
	if got := ServiceName("app", 0); got != "app-rw" {
		t.Errorf("unset instances: %s", got)
	}
	if got := ServiceName("app", 3); got != "app-ro" {
		t.Errorf("with standbys: %s", got)
	}
	d := Database{Namespace: "env-1", Name: "ledger", Instances: 2}
	if tg := d.Target(); tg.Namespace != "env-1" || tg.Service != "ledger-ro" || tg.Host() != "ledger-ro.env-1.svc" {
		t.Errorf("target %+v host %s", tg, tg.Host())
	}
}

func TestDialResolvesAReadyPodAndOpensAStream(t *testing.T) {
	cs := fake.NewClientset(
		slice("env-1", "ledger-ro", 5432, "!ledger-1", "ledger-2"),
		// Another service's slice in the namespace is not considered.
		slice("env-1", "ledger-rw", 5432, "ledger-3"),
	)
	pods := &fakePods{serve: func(string, string, int) (io.ReadWriteCloser, error) {
		c, s := pipeStream()
		go func() { _, _ = io.Copy(s, s) }() // echo
		return c, nil
	}}
	c := NewWithClientset(cs, pods)
	conn, err := c.Dial(context.Background(), Target{Namespace: "env-1", Service: "ledger-ro"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := pods.got(); len(got) != 1 || got[0] != "env-1/ledger-2:5432" {
		t.Fatalf("forwarded to %v, want only the ready pod behind ledger-ro", got)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo %q %v", buf, err)
	}
	if conn.RemoteAddr().String() != "env-1/ledger-2:5432" {
		t.Errorf("remote %s", conn.RemoteAddr())
	}
}

func TestDialSpreadsAcrossReplicasAndOneStreamPerConnection(t *testing.T) {
	cs := fake.NewClientset(slice("ns", "db-ro", 5432, "a", "b", "c"))
	pods := &fakePods{serve: func(string, string, int) (io.ReadWriteCloser, error) {
		c, _ := pipeStream()
		return c, nil
	}}
	c := NewWithClientset(cs, pods)
	prev := pick
	i := -1
	pick = func(n int) int { i++; return i % n }
	t.Cleanup(func() { pick = prev })
	for range 3 {
		conn, err := c.Dial(context.Background(), Target{Namespace: "ns", Service: "db-ro"})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	want := []string{"ns/a:5432", "ns/b:5432", "ns/c:5432"}
	if got := pods.got(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dials %v, want one stream per connection, rotating: %v", got, want)
	}
}

func TestDialErrors(t *testing.T) {
	ctx := context.Background()
	noPods := &fakePods{serve: func(string, string, int) (io.ReadWriteCloser, error) {
		t.Error("a pod was dialled")
		return nil, errors.New("unexpected")
	}}

	t.Run("no endpoint", func(t *testing.T) {
		c := NewWithClientset(fake.NewClientset(), noPods)
		_, err := c.Dial(ctx, Target{Namespace: "ns", Service: "db-rw"})
		if !errors.Is(err, ErrNoReadyEndpoint) {
			t.Errorf("err = %v, want ErrNoReadyEndpoint", err)
		}
	})
	t.Run("only unready endpoints", func(t *testing.T) {
		c := NewWithClientset(fake.NewClientset(slice("ns", "db-rw", 5432, "!a", "!b")), noPods)
		if _, err := c.Dial(ctx, Target{Namespace: "ns", Service: "db-rw"}); !errors.Is(err, ErrNoReadyEndpoint) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("endpoint without a pod", func(t *testing.T) {
		s := slice("ns", "db-rw", 5432, "a")
		s.Endpoints[0].TargetRef = nil
		c := NewWithClientset(fake.NewClientset(s), noPods)
		if _, err := c.Dial(ctx, Target{Namespace: "ns", Service: "db-rw"}); !errors.Is(err, ErrNoReadyEndpoint) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("forbidden endpointslices names the rules", func(t *testing.T) {
		cs := fake.NewClientset()
		cs.PrependReactor("list", "endpointslices", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "discovery.k8s.io", Resource: "endpointslices"}, "", errors.New("no"))
		})
		_, err := NewWithClientset(cs, noPods).Dial(ctx, Target{Namespace: "ns", Service: "db-rw"})
		if err == nil || !strings.Contains(err.Error(), "PolicyRules") {
			t.Errorf("err = %v, want the Role-rules hint", err)
		}
	})
	t.Run("forward refused", func(t *testing.T) {
		pods := &fakePods{serve: func(string, string, int) (io.ReadWriteCloser, error) {
			return nil, errors.New(`pods "a" is forbidden: cannot create resource "pods/portforward"`)
		}}
		c := NewWithClientset(fake.NewClientset(slice("ns", "db-rw", 5432, "a")), pods)
		_, err := c.Dial(ctx, Target{Namespace: "ns", Service: "db-rw"})
		if err == nil || !strings.Contains(err.Error(), "ns/a") || !strings.Contains(err.Error(), "PolicyRules") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("empty target", func(t *testing.T) {
		if _, err := NewWithClientset(fake.NewClientset(), noPods).Dial(ctx, Target{}); err == nil {
			t.Error("want an error")
		}
	})
	t.Run("network", func(t *testing.T) {
		df := NewWithClientset(fake.NewClientset(), noPods).DialFunc(Target{Namespace: "ns", Service: "x"})
		if _, err := df(ctx, "unix", "/tmp/s"); err == nil {
			t.Error("a unix network must be refused")
		}
	})
}

func TestSlicePortFollowsThePodSide(t *testing.T) {
	// A Service on 5432 whose pods listen on 6432: the slice carries the pod port.
	c := NewWithClientset(fake.NewClientset(slice("ns", "db-rw", 6432, "a")), &fakePods{serve: func(string, string, int) (io.ReadWriteCloser, error) {
		cl, _ := pipeStream()
		return cl, nil
	}})
	conn, err := c.Dial(context.Background(), Target{Namespace: "ns", Service: "db-rw"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if conn.RemoteAddr().String() != "ns/a:6432" {
		t.Errorf("remote %s", conn.RemoteAddr())
	}
	// Several ports and none is 5432: ambiguous, not guessed.
	s := slice("ns", "db-rw", 1111, "a")
	s.Ports = append(s.Ports, discoveryv1.EndpointPort{Port: ptr(int32(2222))})
	_, err = NewWithClientset(fake.NewClientset(s), &fakePods{}).Dial(context.Background(), Target{Namespace: "ns", Service: "db-rw"})
	if !errors.Is(err, ErrNoReadyEndpoint) {
		t.Errorf("err = %v", err)
	}
}

func TestDialContextCancelsTheResolve(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("list", "endpointslices", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.Canceled
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewWithClientset(cs, &fakePods{}).Dial(ctx, Target{Namespace: "ns", Service: "x"}); err == nil {
		t.Fatal("want an error")
	}
}

// --- net.Conn semantics ---

func TestConnDeadlines(t *testing.T) {
	cl, srv := pipeStream()
	defer srv.Close()
	c := newConn(cl, "ns/p:5432")
	defer c.Close()

	// A read deadline in the past fails at once, and moving it back out
	// lets the connection read again (pgx cancels a query exactly this way).
	_ = c.SetReadDeadline(time.Now().Add(-time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("past deadline: %v", err)
	}
	_ = c.SetReadDeadline(time.Time{})
	go func() { _, _ = srv.Write([]byte("hello")) }()
	buf := make([]byte, 3)
	if n, err := c.Read(buf); err != nil || string(buf[:n]) != "hel" {
		t.Fatalf("read %q %v", buf[:n], err)
	}
	// The unread remainder survives into the next Read.
	if n, err := c.Read(buf); err != nil || string(buf[:n]) != "lo" {
		t.Fatalf("remainder %q %v", buf[:n], err)
	}

	// A pending read is released by a deadline that arrives later.
	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	start := time.Now()
	if _, err := c.Read(buf); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("pending read: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the deadline did not release the read")
	}
	var ne net.Error
	if _, err := func() (int, error) { _ = c.SetReadDeadline(time.Now().Add(-time.Second)); return c.Read(buf) }(); !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("deadline error must be a net.Error timeout: %v", err)
	}
}

func TestConnWriteDeadlineAndClose(t *testing.T) {
	cl, srv := pipeStream() // net.Pipe: a write blocks until the server reads
	c := newConn(cl, "ns/p:5432")
	_ = c.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := c.Write([]byte("blocked")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("blocked write: %v", err)
	}
	// A timed-out write may have been half sent, so the connection is spent.
	_ = c.SetWriteDeadline(time.Time{})
	if _, err := c.Write([]byte("again")); err == nil {
		t.Error("a write after a timed-out write must fail")
	}
	_ = srv.Close()

	// Close wakes a blocked Read, and is idempotent.
	cl2, srv2 := pipeStream()
	defer srv2.Close()
	c2 := newConn(cl2, "x")
	errc := make(chan error, 1)
	go func() { _, err := c2.Read(make([]byte, 1)); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	_ = c2.Close()
	_ = c2.Close()
	select {
	case err := <-errc:
		if err == nil {
			t.Error("Read after Close returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not wake the Read")
	}
	if _, err := c2.Write([]byte("x")); err == nil {
		t.Error("Write after Close")
	}
}

func TestConnReadsEOFAndStreamErrors(t *testing.T) {
	cl, srv := pipeStream()
	c := newConn(cl, "x")
	defer c.Close()
	go func() { _, _ = srv.Write([]byte("last")); _ = srv.Close() }()
	b, err := io.ReadAll(c)
	if err != nil || string(b) != "last" {
		t.Fatalf("%q %v", b, err)
	}
}

// --- credentials, config and TLS ---

type pki struct {
	caPEM []byte
	ca    *x509.Certificate
	key   *ecdsa.PrivateKey
}

func newCA(t *testing.T) pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "cnpg-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	return pki{caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca: ca, key: key}
}

func (p pki) serverCert(t *testing.T, dnsName string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: dnsName}, DNSNames: []string{dnsName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, p.ca, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func secrets(ns, db string, ca []byte) []runtime.Object {
	return []runtime.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: dbgrants.SecretName(db)}, Type: corev1.SecretTypeBasicAuth,
			Data: map[string][]byte{"username": []byte("forge_ro"), "password": []byte("s3cret")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: CASecretName(db)},
			Data: map[string][]byte{"ca.crt": ca, "ca.key": []byte("PRIVATE")}},
	}
}

func TestCredentials(t *testing.T) {
	ctx := context.Background()
	ca := newCA(t)
	db := Database{Namespace: "env-1", Name: "ledger", Instances: 2}

	creds, err := NewWithClientset(fake.NewClientset(secrets("env-1", "ledger", ca.caPEM)...), nil).Credentials(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Username != "forge_ro" || creds.Password != "s3cret" || string(creds.CAPEM) != string(ca.caPEM) {
		t.Errorf("creds %+v", creds)
	}

	t.Run("a missing role secret says the database predates the role", func(t *testing.T) {
		cs := fake.NewClientset(secrets("env-1", "ledger", ca.caPEM)[1])
		_, err := NewWithClientset(cs, nil).Credentials(ctx, db)
		if !errors.Is(err, ErrNoReadOnlyCredentials) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no password", func(t *testing.T) {
		objs := secrets("env-1", "ledger", ca.caPEM)
		objs[0].(*corev1.Secret).Data["password"] = nil
		if _, err := NewWithClientset(fake.NewClientset(objs...), nil).Credentials(ctx, db); !errors.Is(err, ErrNoReadOnlyCredentials) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a username-less secret falls back to the role name", func(t *testing.T) {
		objs := secrets("env-1", "ledger", ca.caPEM)
		delete(objs[0].(*corev1.Secret).Data, "username")
		c, err := NewWithClientset(fake.NewClientset(objs...), nil).Credentials(ctx, db)
		if err != nil || c.Username != dbgrants.RoleName {
			t.Errorf("%+v %v", c, err)
		}
	})
	t.Run("a missing CA names the secret", func(t *testing.T) {
		cs := fake.NewClientset(secrets("env-1", "ledger", ca.caPEM)[0])
		_, err := NewWithClientset(cs, nil).Credentials(ctx, db)
		if err == nil || !strings.Contains(err.Error(), "ledger-ca") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("only the two named secrets are read", func(t *testing.T) {
		cs := fake.NewClientset(secrets("env-1", "ledger", ca.caPEM)...)
		if _, err := NewWithClientset(cs, nil).Credentials(ctx, db); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, a := range cs.Actions() {
			if g, ok := a.(clienttesting.GetAction); ok {
				got = append(got, a.GetVerb()+" "+a.GetResource().Resource+"/"+g.GetName())
			} else {
				got = append(got, a.GetVerb()+" "+a.GetResource().Resource+" (not a get)")
			}
		}
		if want := "get secrets/ledger-forge-ro,get secrets/ledger-ca"; strings.Join(got, ",") != want {
			t.Errorf("api calls %v, want %s", got, want)
		}
	})
}

// A verify-full handshake inside the tunnel: the "pod" is a TLS server whose
// certificate the cluster CA signed for <service>.<ns>.svc.
func TestConnConfigVerifiesTheServerInsideTheTunnel(t *testing.T) {
	ca := newCA(t)
	db := Database{Namespace: "env-1", Name: "ledger", Instances: 2}
	host := db.Target().Host()

	serve := func(cert tls.Certificate) *fakePods {
		return &fakePods{serve: func(string, string, int) (io.ReadWriteCloser, error) {
			cl, srv := pipeStream()
			go func() {
				tc := tls.Server(srv, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if err := tc.Handshake(); err == nil {
					_, _ = tc.Write([]byte("ok"))
				}
				_ = tc.Close()
			}()
			return cl, nil
		}}
	}
	handshake := func(t *testing.T, pods *fakePods) error {
		t.Helper()
		cs := fake.NewClientset(append(secrets("env-1", "ledger", ca.caPEM), slice("env-1", "ledger-ro", 5432, "ledger-2"))...)
		c := NewWithClientset(cs, pods)
		cfg, err := c.ConnConfig(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.User != "forge_ro" || cfg.Database != "ledger" || cfg.Host != host || cfg.TLSConfig.ServerName != host {
			t.Fatalf("config user=%s db=%s host=%s sni=%s", cfg.User, cfg.Database, cfg.Host, cfg.TLSConfig.ServerName)
		}
		raw, err := cfg.DialFunc(context.Background(), "tcp", cfg.Host+":5432")
		if err != nil {
			t.Fatal(err)
		}
		// Close the raw stream, not the tls.Conn: its close_notify would
		// block on the unread in-memory pipe.
		defer raw.Close()
		return tls.Client(raw, cfg.TLSConfig).Handshake()
	}

	if err := handshake(t, serve(ca.serverCert(t, host))); err != nil {
		t.Errorf("a certificate for %s signed by the CA: %v", host, err)
	}
	if err := handshake(t, serve(ca.serverCert(t, "someone-else.svc"))); err == nil {
		t.Error("a certificate for another name must fail verify-full")
	}
	other := newCA(t)
	if err := handshake(t, serve(other.serverCert(t, host))); err == nil {
		t.Error("a certificate from another CA must fail verify-full")
	}
}

func TestConnConfigDoesNotLeakThePassword(t *testing.T) {
	c := NewWithClientset(fake.NewClientset(), nil)
	_, err := c.ConnConfigFor(Database{Namespace: "ns", Name: "db"}, &Credentials{Username: "u", Password: "pw", CAPEM: []byte("not pem")})
	if err == nil || strings.Contains(err.Error(), "pw") {
		t.Errorf("err = %v", err)
	}
	// A password that breaks URL parsing must not be echoed either.
	_, err = c.ConnConfigFor(Database{Namespace: "ns", Name: "db"}, &Credentials{Username: "u", Password: "p%zz\x7f", CAPEM: newCA(t).caPEM})
	if err != nil && strings.Contains(err.Error(), "p%zz") {
		t.Errorf("err leaked the password: %v", err)
	}
}

// --- the real port-forward protocol ---

type fakeStream struct {
	headers http.Header
	r       io.Reader
	w       io.Writer
	closed  atomic.Bool
	reset   atomic.Bool
}

func (s *fakeStream) Read(b []byte) (int, error)  { return s.r.Read(b) }
func (s *fakeStream) Write(b []byte) (int, error) { return s.w.Write(b) }
func (s *fakeStream) Close() error                { s.closed.Store(true); return nil }
func (s *fakeStream) Reset() error                { s.reset.Store(true); return nil }
func (s *fakeStream) Headers() http.Header        { return s.headers }
func (s *fakeStream) Identifier() uint32          { return 1 }

type fakeConnection struct {
	mu       sync.Mutex
	created  []*fakeStream
	dataOut  *io.PipeWriter
	dataIn   *io.PipeReader
	errBody  string
	closed   bool
	closedCh chan bool
}

func (c *fakeConnection) CreateStream(h http.Header) (streamhttp.Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &fakeStream{headers: h.Clone()}
	switch h.Get(corev1.StreamType) {
	case corev1.StreamTypeError:
		s.r, s.w = strings.NewReader(c.errBody), io.Discard
	case corev1.StreamTypeData:
		s.r, s.w = c.dataIn, c.dataOut
	}
	c.created = append(c.created, s)
	return s, nil
}
func (c *fakeConnection) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
func (c *fakeConnection) Close() error                       { c.mu.Lock(); c.closed = true; c.mu.Unlock(); return nil }
func (c *fakeConnection) CloseChan() <-chan bool             { return c.closedCh }
func (c *fakeConnection) SetIdleTimeout(time.Duration)       {}
func (c *fakeConnection) RemoveStreams(...streamhttp.Stream) {}

type fakeDialer struct {
	conn     streamhttp.Connection
	protocol string
	err      error
	protos   []string
	block    chan struct{}
}

func (d *fakeDialer) Dial(protocols ...string) (streamhttp.Connection, string, error) {
	d.protos = protocols
	if d.block != nil {
		<-d.block
	}
	return d.conn, d.protocol, d.err
}

func newFakeConnection(errBody string) (*fakeConnection, io.ReadWriteCloser) {
	// server side of the data stream: what the "pod" reads and writes.
	toClientR, toClientW := io.Pipe() // pod -> client
	toPodR, toPodW := io.Pipe()       // client -> pod
	_ = toPodR
	return &fakeConnection{dataIn: toClientR, dataOut: toPodW, errBody: errBody, closedCh: make(chan bool)},
		&podEnd{r: toPodR, w: toClientW}
}

type podEnd struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *podEnd) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *podEnd) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *podEnd) Close() error                { _ = p.r.Close(); return p.w.Close() }

func TestDialStreamsSpeaksThePortForwardProtocol(t *testing.T) {
	conn, pod := newFakeConnection("")
	rwc, err := dialStreams(context.Background(), &fakeDialer{conn: conn, protocol: "portforward.k8s.io"}, 5432)
	if err != nil {
		t.Fatal(err)
	}
	if len(conn.created) != 2 {
		t.Fatalf("created %d streams, want an error stream and a data stream", len(conn.created))
	}
	e, d := conn.created[0], conn.created[1]
	if e.headers.Get(corev1.StreamType) != corev1.StreamTypeError || d.headers.Get(corev1.StreamType) != corev1.StreamTypeData {
		t.Errorf("stream types %q then %q", e.headers.Get(corev1.StreamType), d.headers.Get(corev1.StreamType))
	}
	for _, s := range conn.created {
		if s.headers.Get(corev1.PortHeader) != "5432" || s.headers.Get(corev1.PortForwardRequestIDHeader) != "0" {
			t.Errorf("headers %v", s.headers)
		}
	}
	if !e.closed.Load() {
		t.Error("the error stream is read-only from the client: the write side must be closed")
	}

	// Bytes flow both ways through the data stream.
	go func() { _, _ = pod.Write([]byte("from-pod")) }()
	buf := make([]byte, 8)
	if _, err := io.ReadFull(rwc, buf); err != nil || string(buf) != "from-pod" {
		t.Fatalf("read %q %v", buf, err)
	}
	got := make(chan string, 1)
	go func() { b := make([]byte, 7); _, _ = io.ReadFull(pod, b); got <- string(b) }()
	if _, err := rwc.Write([]byte("to-pod!")); err != nil {
		t.Fatal(err)
	}
	if s := <-got; s != "to-pod!" {
		t.Errorf("pod read %q", s)
	}

	_ = rwc.Close()
	_ = rwc.Close()
	if !d.reset.Load() || !conn.isClosed() {
		t.Errorf("Close must reset the stream (%v) and close the connection (%v)", d.reset.Load(), conn.isClosed())
	}
}

func TestDialStreamsSurfacesThePodsError(t *testing.T) {
	prev := errorDrain
	errorDrain = 2 * time.Second
	t.Cleanup(func() { errorDrain = prev })
	conn, pod := newFakeConnection("an error occurred forwarding 5432 -> 5432: connection refused")
	rwc, err := dialStreams(context.Background(), &fakeDialer{conn: conn, protocol: "portforward.k8s.io"}, 5432)
	if err != nil {
		t.Fatal(err)
	}
	defer rwc.Close()
	_ = pod.Close() // the kubelet ends the data stream after reporting
	_, err = rwc.Read(make([]byte, 1))
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("read err = %v, want the pod's reason, not a bare EOF", err)
	}
}

func TestDialStreamsFailures(t *testing.T) {
	t.Run("dial error", func(t *testing.T) {
		_, err := dialStreams(context.Background(), &fakeDialer{err: errors.New("upgrade refused")}, 5432)
		if err == nil || !strings.Contains(err.Error(), "upgrade refused") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("wrong protocol", func(t *testing.T) {
		conn, _ := newFakeConnection("")
		_, err := dialStreams(context.Background(), &fakeDialer{conn: conn, protocol: "other"}, 5432)
		if err == nil || !conn.isClosed() {
			t.Errorf("err = %v closed = %v", err, conn.isClosed())
		}
	})
	t.Run("context cancel abandons the dial and closes a late connection", func(t *testing.T) {
		conn, _ := newFakeConnection("")
		block := make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := dialStreams(ctx, &fakeDialer{conn: conn, protocol: "portforward.k8s.io", block: block}, 5432)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		close(block)
		deadline := time.Now().Add(5 * time.Second)
		for {
			if conn.isClosed() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("a connection that arrived after the cancel was leaked")
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

// --- end to end against a real Postgres ---

// The whole path: resolve through EndpointSlices, a stream per connection,
// the secret's role, and dbquery.Run on top. TLS is dropped here only because
// pgtest's server has none; TestConnConfigVerifiesTheServerInsideTheTunnel
// covers the verification.
func TestQueryThroughTheTunnelAgainstPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real postgres (pgtest); runs in task test")
	}
	admin, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	pc, err := pgx.ParseConfig(admin)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ConnectConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	role := fmt.Sprintf("kd_ro_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS kd_entries (id int PRIMARY KEY, amount int, secret text);
		INSERT INTO kd_entries VALUES (1, 10, 's') ON CONFLICT DO NOTHING;
		CREATE ROLE %s LOGIN PASSWORD 'pw'; GRANT SELECT (id, amount) ON kd_entries TO %s`, role, role)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON kd_entries FROM %s; DROP ROLE IF EXISTS %s`, role, role))
	})

	dbName := pc.Database // a pgtest name has no hyphen for DatabaseIdentifiers to fold
	addr := net.JoinHostPort(pc.Host, fmt.Sprint(pc.Port))
	pods := &fakePods{serve: func(string, string, int) (io.ReadWriteCloser, error) {
		return net.Dial("tcp", addr)
	}}
	ca := newCA(t)
	objs := secrets("env-1", dbName, ca.caPEM)
	objs[0].(*corev1.Secret).Data["username"] = []byte(role)
	objs[0].(*corev1.Secret).Data["password"] = []byte("pw")
	objs = append(objs, slice("env-1", dbName+"-ro", 5432, dbName+"-2"))
	c := NewWithClientset(fake.NewClientset(objs...), pods)
	db := Database{Namespace: "env-1", Name: dbName, Instances: 2}

	cfg, err := c.ConnConfig(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TLSConfig = nil
	cfg.Fallbacks = nil
	cn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect through the tunnel: %v", err)
	}
	defer cn.Close(ctx)
	res, err := dbquery.Run(ctx, cn, "select id, amount from kd_entries order by id", dbquery.Options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.RowCount != 1 {
		t.Errorf("rows = %d", res.RowCount)
	}
	if got := pods.got(); len(got) != 1 || got[0] != "env-1/"+dbName+"-2:5432" {
		t.Errorf("forwarded %v", got)
	}
	// The role's grants are the boundary: an ungranted column is refused.
	if _, err := dbquery.Run(ctx, cn, "select secret from kd_entries", dbquery.Options{}); err == nil {
		t.Error("an ungranted column must be refused")
	}
	// A timeout cancels through the connection deadline, not a hang.
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = cn.Exec(short, "select pg_sleep(10)")
	if time.Since(start) > 5*time.Second {
		t.Error("a context deadline must interrupt a query through the tunnel")
	}
}

// --- bring-your-own-route (ConnConfigForAddr) ---

func TestServerNameForAddr(t *testing.T) {
	// The -rw Service whatever the instance count: it is the name every CNPG
	// server certificate carries and the one that always exists.
	for _, instances := range []int32{0, 1, 3} {
		if got := ServerNameForAddr(Database{Namespace: "env-1", Name: "ledger", Instances: instances}); got != "ledger-rw.env-1.svc" {
			t.Errorf("instances=%d: %q", instances, got)
		}
	}
}

func TestConnConfigForAddr(t *testing.T) {
	ca := newCA(t)
	db := Database{Namespace: "env-1", Name: "ledger", Instances: 2}
	creds := &Credentials{Username: "forge_ro", Password: "s3cret", CAPEM: ca.caPEM}

	cfg, err := ConnConfigForAddr(db, creds, "localhost:6543", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "localhost" || cfg.Port != 6543 || cfg.User != "forge_ro" || cfg.Password != "s3cret" || cfg.Database != "ledger" {
		t.Errorf("config host=%s port=%d user=%s db=%s", cfg.Host, cfg.Port, cfg.User, cfg.Database)
	}
	if cfg.TLSConfig == nil || cfg.TLSConfig.ServerName != "ledger-rw.env-1.svc" || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.RootCAs == nil {
		t.Errorf("TLS %+v", cfg.TLSConfig)
	}
	if cfg.DialFunc != nil && cfg.LookupFunc != nil {
		// The default (empty) config has pgx's own dialer and resolver.
		t.Log("note: pgx defaults are populated by ParseConfig")
	}

	if cfg, err := ConnConfigForAddr(db, creds, "10.0.0.5:5432", SSLModeRequire); err != nil || cfg.TLSConfig == nil || !cfg.TLSConfig.InsecureSkipVerify {
		t.Errorf("require: %v %+v", err, cfg)
	}
	if cfg, err := ConnConfigForAddr(db, creds, "[::1]:5432", SSLModeDisable); err != nil || cfg.TLSConfig != nil || cfg.Host != "::1" {
		t.Errorf("disable: %v %+v", err, cfg)
	}

	for _, bad := range []string{"", "localhost", ":5432", "h:0", "h:99999", "h:x", "postgres://h:5432"} {
		if _, err := ConnConfigForAddr(db, creds, bad, ""); err == nil {
			t.Errorf("address %q accepted", bad)
		}
	}
	if _, err := ConnConfigForAddr(db, creds, "h:5432", "prefer"); err == nil {
		t.Error("unknown sslmode accepted")
	}
	if _, err := ConnConfigForAddr(db, &Credentials{Username: "u", Password: "pw", CAPEM: []byte("junk")}, "h:5432", ""); err == nil || strings.Contains(err.Error(), "pw") {
		t.Errorf("a CA that is not PEM must fail verify-full without echoing the password: %v", err)
	}
	// require and disable need no CA.
	if _, err := ConnConfigForAddr(db, &Credentials{Username: "u", Password: "pw"}, "h:5432", SSLModeRequire); err != nil {
		t.Errorf("require without a CA: %v", err)
	}
}

// verify-full through a route that is not the cluster: a TLS server on
// localhost whose certificate names <db>-rw.<ns>.svc verifies; a certificate
// for any other name, or from another CA, does not.
func TestConnConfigForAddrVerifiesTheServiceName(t *testing.T) {
	ca := newCA(t)
	db := Database{Namespace: "env-1", Name: "ledger", Instances: 1}
	creds := &Credentials{Username: "forge_ro", Password: "pw", CAPEM: ca.caPEM}

	handshake := func(t *testing.T, cert tls.Certificate) error {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
			_ = tc.Handshake()
			_ = c.Close()
		}()
		cfg, err := ConnConfigForAddr(db, creds, ln.Addr().String(), "")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		return tls.Client(raw, cfg.TLSConfig).Handshake()
	}
	if err := handshake(t, ca.serverCert(t, "ledger-rw.env-1.svc")); err != nil {
		t.Errorf("a certificate for the -rw service host: %v", err)
	}
	if err := handshake(t, ca.serverCert(t, "localhost")); err == nil {
		t.Error("a certificate for localhost must not satisfy the default server name")
	}
	if err := handshake(t, newCA(t).serverCert(t, "ledger-rw.env-1.svc")); err == nil {
		t.Error("a certificate from another CA must fail verify-full")
	}
}

func dashSecrets(ns, db string, ca []byte) []runtime.Object {
	return append(secrets(ns, db, ca),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: dbgrants.DashboardSecretName(db)}, Type: corev1.SecretTypeBasicAuth,
			Data: map[string][]byte{"username": []byte("forge_dash"), "password": []byte("dash-pw")}})
}

func TestCredentialsForDashboardRole(t *testing.T) {
	ctx := context.Background()
	ca := newCA(t)
	db := Database{Namespace: "env-1", Name: "ledger", Instances: 2}

	cs := fake.NewClientset(dashSecrets("env-1", "ledger", ca.caPEM)...)
	creds, err := NewWithClientset(cs, nil).CredentialsFor(ctx, db, RoleDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Username != "forge_dash" || creds.Password != "dash-pw" || string(creds.CAPEM) != string(ca.caPEM) {
		t.Errorf("creds %+v", creds)
	}
	// Reads the dashboard role's Secret and the CA, and never forge_ro's.
	var got []string
	for _, a := range cs.Actions() {
		if g, ok := a.(clienttesting.GetAction); ok {
			got = append(got, g.GetName())
		}
	}
	sort.Strings(got)
	if want := []string{"ledger-ca", "ledger-forge-dash"}; !reflect.DeepEqual(got, want) {
		t.Errorf("secrets read = %v, want exactly %v", got, want)
	}

	// The default role is unchanged.
	ro, err := NewWithClientset(fake.NewClientset(dashSecrets("env-1", "ledger", ca.caPEM)...), nil).Credentials(ctx, db)
	if err != nil || ro.Username != "forge_ro" {
		t.Errorf("Credentials = %+v, %v; want forge_ro", ro, err)
	}

	t.Run("a database that never enabled dashboards", func(t *testing.T) {
		_, err := NewWithClientset(fake.NewClientset(secrets("env-1", "ledger", ca.caPEM)...), nil).CredentialsFor(ctx, db, RoleDashboard)
		if !errors.Is(err, ErrNoDashboardCredentials) || errors.Is(err, ErrNoReadOnlyCredentials) {
			t.Fatalf("err = %v, want ErrNoDashboardCredentials only", err)
		}
		if !strings.Contains(err.Error(), "ledger-forge-dash") || !strings.Contains(err.Error(), "dashboards") {
			t.Errorf("err does not name the Secret and the declaration: %v", err)
		}
	})
	t.Run("no password", func(t *testing.T) {
		objs := dashSecrets("env-1", "ledger", ca.caPEM)
		objs[2].(*corev1.Secret).Data["password"] = nil
		if _, err := NewWithClientset(fake.NewClientset(objs...), nil).CredentialsFor(ctx, db, RoleDashboard); !errors.Is(err, ErrNoDashboardCredentials) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a username-less secret falls back to the dashboard role", func(t *testing.T) {
		objs := dashSecrets("env-1", "ledger", ca.caPEM)
		delete(objs[2].(*corev1.Secret).Data, "username")
		c, err := NewWithClientset(fake.NewClientset(objs...), nil).CredentialsFor(ctx, db, RoleDashboard)
		if err != nil || c.Username != dbgrants.DashboardRoleName {
			t.Errorf("%+v %v", c, err)
		}
	})
}
