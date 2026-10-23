package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// The OpenBao connection verifies the store's certificate against the CA the
// component publishes. These tests run a real TLS server (the "pod") behind the
// seam that stands in for the port-forward, so what is exercised is the real
// handshake, not a stub of it.

type testPKI struct {
	caPEM string
	ca    *x509.Certificate
	key   *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, cn string) testPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	return testPKI{caPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), ca: ca, key: key}
}

// serverCert issues a leaf for dnsNames, signed by the CA.
func (p testPKI) serverCert(t *testing.T, dnsNames ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: dnsNames[0]},
		DNSNames: dnsNames, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// tlsStore serves a counting handler over TLS and returns its host:port.
func tlsStore(t *testing.T, cert tls.Certificate, requests *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func tunnelFixture(t *testing.T, addr, caPEM string) (*tunnelTransport, *fakeKube) {
	t.Helper()
	k := newFakeKube()
	k.forwardTo = addr
	if caPEM != "" {
		k.configMaps = map[string]string{"openbao/openbao-ca/ca.crt": caPEM}
	}
	return newTunnelTransport(k, storeEnv().OpenBao), k
}

func TestTunnel_VerifiesTheStoreAgainstThePublishedCA(t *testing.T) {
	pki := newTestCA(t, "openbao-ca")
	var n atomic.Int32
	addr := tlsStore(t, pki.serverCert(t, "openbao.openbao.svc"), &n)
	tr, k := tunnelFixture(t, addr, pki.caPEM)
	t.Cleanup(func() { _ = tr.Close() })

	status, body, err := tr.do(context.Background(), http.MethodGet, "sys/health", nil, "tok", nil)
	if err != nil || status != 200 || body["data"] == nil {
		t.Fatalf("status %d body %v err %v", status, body, err)
	}
	if len(k.forwards) != 1 || k.forwards[0] != "openbao/openbao:8200" {
		t.Errorf("forwarded %v, want the store's Service port once", k.forwards)
	}
	// A second request reuses the tunnel.
	if _, _, err := tr.do(context.Background(), http.MethodGet, "sys/health", nil, "tok", nil); err != nil {
		t.Fatal(err)
	}
	if len(k.forwards) != 1 {
		t.Errorf("a second request opened a second tunnel: %v", k.forwards)
	}
	_ = tr.Close()
	_ = tr.Close()
	if k.closed != 1 {
		t.Errorf("the tunnel was closed %d times, want once", k.closed)
	}
}

func TestTunnel_RefusesACertificateTheCADidNotSign(t *testing.T) {
	published := newTestCA(t, "openbao-ca")
	attacker := newTestCA(t, "someone-else")
	var n atomic.Int32
	addr := tlsStore(t, attacker.serverCert(t, "openbao.openbao.svc"), &n)
	tr, _ := tunnelFixture(t, addr, published.caPEM)
	t.Cleanup(func() { _ = tr.Close() })

	_, _, err := tr.do(context.Background(), http.MethodPut, "auth/jwt-hub/login", nil, "", map[string]any{"jwt": "sa-token-canary"})
	if err == nil {
		t.Fatal("a certificate from another CA verified")
	}
	if !strings.Contains(err.Error(), "did not verify against the CA in configmap openbao/openbao-ca") || !strings.Contains(err.Error(), "Nothing was sent") {
		t.Errorf("error = %v", err)
	}
	if n.Load() != 0 {
		t.Errorf("the store saw %d request(s) over a connection that never verified", n.Load())
	}
}

func TestTunnel_RefusesACertificateForAnotherName(t *testing.T) {
	pki := newTestCA(t, "openbao-ca")
	var n atomic.Int32
	addr := tlsStore(t, pki.serverCert(t, "somewhere-else.example"), &n)
	tr, _ := tunnelFixture(t, addr, pki.caPEM)
	t.Cleanup(func() { _ = tr.Close() })

	_, _, err := tr.do(context.Background(), http.MethodGet, "sys/health", nil, "tok", nil)
	if err == nil || !strings.Contains(err.Error(), "did not verify") {
		t.Fatalf("a certificate for the wrong name verified (err %v)", err)
	}
	if n.Load() != 0 {
		t.Errorf("the store saw %d request(s)", n.Load())
	}
}

func TestTunnel_NoPublishedCAMeansNoConnectionAtAll(t *testing.T) {
	pki := newTestCA(t, "openbao-ca")
	var n atomic.Int32
	addr := tlsStore(t, pki.serverCert(t, "openbao.openbao.svc"), &n)
	tr, k := tunnelFixture(t, addr, "")

	_, _, err := tr.do(context.Background(), http.MethodGet, "sys/health", nil, "tok", nil)
	if !errors.Is(err, errKubeConfigMapNotFound) || !strings.Contains(err.Error(), "forge env deploy") || !strings.Contains(err.Error(), "will not talk to the store without verifying") {
		t.Fatalf("error = %v", err)
	}
	if len(k.forwards) != 0 {
		t.Errorf("a tunnel was opened without a CA: %v", k.forwards)
	}
}

func TestTunnel_AGarbageCAIsRefused(t *testing.T) {
	tr, k := tunnelFixture(t, "127.0.0.1:1", "not a certificate")
	_, _, err := tr.do(context.Background(), http.MethodGet, "sys/health", nil, "tok", nil)
	if err == nil || !strings.Contains(err.Error(), "holds no PEM certificate") {
		t.Fatalf("error = %v", err)
	}
	if len(k.forwards) != 0 {
		t.Errorf("a tunnel was opened with an unusable CA")
	}
}

func TestTunnel_ARenderWithoutACAIsRefusedBeforeTheCluster(t *testing.T) {
	k := newFakeKube()
	bao := *storeEnv().OpenBao
	bao.CAConfigMap = ""
	tr := newTunnelTransport(k, &bao)
	_, _, err := tr.do(context.Background(), http.MethodGet, "sys/health", nil, "tok", nil)
	if err == nil || !strings.Contains(err.Error(), "openbao.ca_configmap") {
		t.Fatalf("error = %v", err)
	}
	if len(k.forwards) != 0 {
		t.Error("the cluster was contacted")
	}
}

func TestTunnel_DoesNotFollowARedirectWithTheToken(t *testing.T) {
	pki := newTestCA(t, "openbao-ca")
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pki.serverCert(t, "openbao.openbao.svc")}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	tr, _ := tunnelFixture(t, srv.Listener.Addr().String(), pki.caPEM)
	t.Cleanup(func() { _ = tr.Close() })

	status, _, err := tr.do(context.Background(), http.MethodGet, "secret/data/x", nil, "writer-token", nil)
	if err != nil || status != http.StatusTemporaryRedirect {
		t.Fatalf("status %d err %v: the redirect should be the answer, not followed", status, err)
	}
	if elsewhere.Load() != 0 {
		t.Error("the redirect target received a request that carried the token")
	}
}

// The whole write path over the verifying transport: sign in, write, and the
// canary never reaches output.
func TestTunnel_WriterPathOverVerifiedTLS(t *testing.T) {
	pki := newTestCA(t, "openbao-ca")
	var logins, writes atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/auth/jwt-hub/login"):
			logins.Add(1)
			_, _ = w.Write([]byte(`{"auth":{"client_token":"b.tok"}}`))
		case r.Header.Get("X-Vault-Token") == "b.tok" && strings.HasPrefix(r.URL.Path, "/v1/secret/data/tenants/acme/prod/API_KEY"):
			writes.Add(1)
			_, _ = w.Write([]byte(`{"data":{"version":1}}`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pki.serverCert(t, "openbao.openbao.svc")}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	f := installStoreFixture(t, storeEnv())
	f.kube.forwardTo = srv.Listener.Addr().String()
	f.kube.configMaps = map[string]string{"openbao/openbao-ca/ca.crt": pki.caPEM}
	// Route the OpenBao source through the real tunnel transport.
	prev := openStoreSource
	openStoreSource = func(ctx context.Context, envName string, e *KCLEntities, ref storeRef, o storeOptions, writing bool) (storeSource, error) {
		if ref.Kind != "openbao" {
			return prev(ctx, envName, e, ref, o, writing)
		}
		return openBaoStoreSourceWith(envName, e, o, func(string) (kubeAccess, baoTransport, error) {
			return f.kube, newTunnelTransport(f.kube, e.OpenBao), nil
		})
	}
	t.Cleanup(func() { openStoreSource = prev })

	var out bytes.Buffer
	err := runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader(canary), &out)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if logins.Load() != 1 || writes.Load() != 1 {
		t.Errorf("logins %d writes %d", logins.Load(), writes.Load())
	}
	assertNoLeak(t, "set over verified TLS", out.String())
	if f.kube.closed != 1 {
		t.Errorf("the tunnel was closed %d times after the command, want once", f.kube.closed)
	}
}

// The default dial is never a plain connection: the transport it builds has a
// root pool and a server name, and no InsecureSkipVerify.
func TestTunnel_TheClientConfigPinsTheCAAndTheName(t *testing.T) {
	pki := newTestCA(t, "openbao-ca")
	var n atomic.Int32
	addr := tlsStore(t, pki.serverCert(t, "openbao.openbao.svc"), &n)
	tr, _ := tunnelFixture(t, addr, pki.caPEM)
	t.Cleanup(func() { _ = tr.Close() })
	c, err := tr.connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.Transport.(*http.Transport).TLSClientConfig
	if cfg.InsecureSkipVerify || cfg.RootCAs == nil || cfg.ServerName != "openbao.openbao.svc" || cfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("tls config = %+v", cfg)
	}
}

// --- the pod a tunnel ends at -------------------------------------------------

func podFor(name string, ready bool, phase corev1.PodPhase, deleting bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "openbao", Labels: map[string]string{"app.kubernetes.io/name": "openbao"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "bao", Ports: []corev1.ContainerPort{{Name: "api", ContainerPort: 8200}}}}},
		Status:     corev1.PodStatus{Phase: phase},
	}
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}
	if deleting {
		now := metav1.Now()
		p.DeletionTimestamp = &now
	}
	return p
}

func TestPickForwardTarget_ChoosesAReadyPodAndResolvesTheNamedPort(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "openbao", Namespace: "openbao"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app.kubernetes.io/name": "openbao"},
			Ports:    []corev1.ServicePort{{Name: "api", Port: 8200, TargetPort: intstr.FromString("api")}},
		},
	}
	cs := fake.NewClientset(svc,
		podFor("openbao-0", false, corev1.PodRunning, false),
		podFor("openbao-1", true, corev1.PodPending, false),
		podFor("openbao-2", true, corev1.PodRunning, true),
		podFor("openbao-3", true, corev1.PodRunning, false),
	)
	pod, port, err := pickForwardTarget(context.Background(), cs, "openbao", "openbao", 8200)
	if err != nil || pod != "openbao-3" || port != 8200 {
		t.Fatalf("pod %q port %d err %v", pod, port, err)
	}
}

func TestPickForwardTarget_ExplainsEachWayItCanFail(t *testing.T) {
	sel := map[string]string{"app.kubernetes.io/name": "openbao"}
	svc := func(sp corev1.ServicePort) *corev1.Service {
		return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "openbao", Namespace: "openbao"}, Spec: corev1.ServiceSpec{Selector: sel, Ports: []corev1.ServicePort{sp}}}
	}
	api := corev1.ServicePort{Name: "api", Port: 8200, TargetPort: intstr.FromInt32(8200)}
	cases := []struct {
		name string
		objs []any
		port int
		want string
	}{
		{"no service", nil, 8200, "forge env deploy"},
		{"no such port", []any{svc(api)}, 9999, "has no port 9999"},
		{"no ready pod", []any{svc(api), podFor("openbao-0", false, corev1.PodRunning, false)}, 8200, "no ready pod"},
		{"selector-less service", []any{&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "openbao", Namespace: "openbao"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{api}}}}, 8200, "selects no pods"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cs *fake.Clientset
			if len(c.objs) == 0 {
				cs = fake.NewClientset()
			} else {
				objs := make([]runtime.Object, 0, len(c.objs))
				for _, o := range c.objs {
					objs = append(objs, o.(runtime.Object))
				}
				cs = fake.NewClientset(objs...)
			}
			_, _, err := pickForwardTarget(context.Background(), cs, "openbao", "openbao", c.port)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to say %q", err, c.want)
			}
		})
	}
}
