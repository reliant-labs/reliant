package openbao

// P17 and the public listener, against the real binaries: OpenBao 2.6.2 started
// from the config this package renders (with Public set), and the Envoy that
// Envoy Gateway v1.9.2 runs (1.39.1), configured as the Gateway objects
// configure it:
//
//   - the route table is built from PublicAllowlist, with the same method and
//     path matchers EG emits (safe_regex / exact path, ":method" exact);
//   - the upstream is the Service's own name, re-encrypted, with the CA pinned
//     and the Service name as both SNI and the only accepted SAN
//     (BackendTLSPolicy.validation.hostname and caCertificateRefs);
//   - use_remote_address is on and the client's X-Forwarded-For is removed
//     before routing (ClientTrafficPolicy.headers.earlyRequestHeaders.remove).
//
// What this does not run is Envoy Gateway's own translation of the Kubernetes
// objects into that config. That step was run from the v1.9.2 tag with
// `egctl x translate` on the objects of testdata/public.golden.yaml and the
// result is what this file mirrors; see the SF8 row of the secrets design.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The proxy image Envoy Gateway v1.9.2 deploys (api/v1alpha1.DefaultEnvoyProxyImage).
const smokeEnvoyImage = "docker.io/envoyproxy/envoy:distroless-v1.39.1@sha256:eb2c01c13125d1629637cb4e4cce7207009fb7cc2c8027f9742758549d15b6f4"

func envoyConfig(t *testing.T, s Spec, upstream, sni, caPEM string, sans []string) string {
	t.Helper()
	var routes []any
	for _, m := range s.PublicAllowlist() {
		match := map[string]any{"headers": []any{map[string]any{"name": ":method", "string_match": map[string]any{"exact": m.Method}}}}
		if m.Type == "Exact" {
			match["path"] = m.Value
		} else {
			match["safe_regex"] = map[string]any{"regex": m.Value}
		}
		routes = append(routes, map[string]any{"match": match, "route": map[string]any{"cluster": "openbao", "timeout": "10s"}})
	}
	var sanMatchers []any
	for _, n := range sans {
		sanMatchers = append(sanMatchers, map[string]any{"san_type": "DNS", "matcher": map[string]any{"exact": n}})
	}
	hcm := map[string]any{
		"@type":                            "type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
		"stat_prefix":                      "edge",
		"use_remote_address":               true,
		"normalize_path":                   true,
		"merge_slashes":                    true,
		"path_with_escaped_slashes_action": "REJECT_REQUEST",
		"early_header_mutation_extensions": []any{map[string]any{
			"name": "envoy.http.early_header_mutation.header_mutation",
			"typed_config": map[string]any{
				"@type":     "type.googleapis.com/envoy.extensions.http.early_header_mutation.header_mutation.v3.HeaderMutation",
				"mutations": []any{map[string]any{"remove": "x-forwarded-for"}},
			},
		}},
		"http_filters": []any{map[string]any{"name": "envoy.filters.http.router", "typed_config": map[string]any{"@type": "type.googleapis.com/envoy.extensions.filters.http.router.v3.Router"}}},
		"route_config": map[string]any{"virtual_hosts": []any{map[string]any{"name": "openbao", "domains": []any{"*"}, "routes": routes}}},
	}
	cfg := map[string]any{"static_resources": map[string]any{
		"listeners": []any{map[string]any{
			"name":          "edge",
			"address":       map[string]any{"socket_address": map[string]any{"address": "0.0.0.0", "port_value": 18080}},
			"filter_chains": []any{map[string]any{"filters": []any{map[string]any{"name": "envoy.filters.network.http_connection_manager", "typed_config": hcm}}}},
		}},
		"clusters": []any{map[string]any{
			"name": "openbao", "type": "STRICT_DNS", "connect_timeout": "3s",
			"load_assignment": map[string]any{"cluster_name": "openbao", "endpoints": []any{map[string]any{"lb_endpoints": []any{map[string]any{"endpoint": map[string]any{"address": map[string]any{"socket_address": map[string]any{"address": upstream, "port_value": s.Public.Port}}}}}}}},
			"transport_socket": map[string]any{
				"name": "envoy.transport_sockets.tls",
				"typed_config": map[string]any{
					"@type": "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext",
					"sni":   sni,
					"common_tls_context": map[string]any{"validation_context": map[string]any{
						"trusted_ca":                    map[string]any{"inline_string": caPEM},
						"match_typed_subject_alt_names": sanMatchers,
					}},
				},
			},
		}},
	}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPublicSmoke_EnvoyAndOpenBao(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: boots OpenBao and Envoy containers; runs in task test")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	for _, img := range []string{smokeOpenBaoImage, smokeEnvoyImage} {
		if exec.Command("docker", "image", "inspect", img).Run() != nil {
			if out, err := exec.Command("docker", "pull", img).CombinedOutput(); err != nil {
				t.Skipf("cannot pull %s: %v\n%s", img, err, out)
			}
		}
	}

	// A private network with fixed addresses, so the trusted range can be exactly
	// the proxy and an untrusted peer exists to be refused.
	sfx := fmt.Sprintf("%d", time.Now().UnixNano()%1e9)
	network := "bao-public-" + sfx
	octet := 16 + int(time.Now().UnixNano()%200)
	subnet := fmt.Sprintf("10.%d.%d.0/24", octet, int(time.Now().UnixNano()/7%250))
	envoyIP := strings.TrimSuffix(subnet, "0/24") + "10"
	if out, err := exec.Command("docker", "network", "create", "--subnet", subnet, network).CombinedOutput(); err != nil {
		t.Skipf("cannot create a docker network on %s: %v\n%s", subnet, err, out)
	}
	var containers []string
	t.Cleanup(func() {
		for _, c := range containers {
			_ = exec.Command("docker", "rm", "-fv", c).Run()
		}
		_ = exec.Command("docker", "network", "rm", network).Run()
	})

	spec := publicSpec()
	spec.Replicas = 1
	spec.Public.TrustedProxyCIDRs = []string{envoyIP + "/32", "fc00::/7"}
	objs, err := Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "config.hcl"), configOf(t, objs), 0o644)

	// OpenBao's server certificate carries the names the rendered Certificate
	// asks cert-manager for; the CA is what the converge Job publishes.
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smoke-ca"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvT := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: spec.Name}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), DNSNames: spec.dnsNames(), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	srvDER, _ := x509.CreateCertificate(rand.Reader, srvT, ca, &srvKey.PublicKey, caKey)
	keyDER, _ := x509.MarshalECPrivateKey(srvKey)
	enc := func(typ string, der []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
	}
	tlsDir := filepath.Join(dir, "tls")
	mustMkdir(t, tlsDir)
	mustWrite(t, filepath.Join(tlsDir, "tls.crt"), enc("CERTIFICATE", srvDER), 0o644)
	mustWrite(t, filepath.Join(tlsDir, "tls.key"), enc("EC PRIVATE KEY", keyDER), 0o644)
	mustWrite(t, filepath.Join(tlsDir, "ca.crt"), enc("CERTIFICATE", caDER), 0o644)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	caPEM := enc("CERTIFICATE", caDER)

	// A second CA, for the "wrong CA" refusal.
	otherKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherT := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "other-ca"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	otherDER, _ := x509.CreateCertificate(rand.Reader, otherT, otherT, &otherKey.PublicKey, otherKey)
	otherPEM := enc("CERTIFICATE", otherDER)

	free := func() int {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		defer func() { _ = l.Close() }()
		return l.Addr().(*net.TCPAddr).Port
	}
	run := func(name string, args ...string) {
		t.Helper()
		out, err := exec.Command("docker", append([]string{"run", "-d", "--name", name}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker run %s: %v\n%s", name, err, out)
		}
		containers = append(containers, name)
	}

	kb := make([]byte, 32)
	_, _ = rand.Read(kb)
	baoName := "bao-pub-" + sfx
	baoPort, pubPort := free(), free()
	run(baoName, "--network", network, "--network-alias", spec.Host(),
		"-p", fmt.Sprintf("127.0.0.1:%d:8200", baoPort), "-p", fmt.Sprintf("127.0.0.1:%d:%d", pubPort, spec.Public.Port),
		"--tmpfs", "/openbao/data:uid=100,gid=1000", "--tmpfs", "/openbao/audit:uid=100,gid=1000",
		"-v", tlsDir+":/openbao/tls:ro", "-v", filepath.Join(dir, "config.hcl")+":/etc/bao/config.hcl:ro",
		"-e", "BAO_STATIC_KEY="+hex.EncodeToString(kb), "-e", "BAO_API_ADDR=https://"+spec.Host()+":8200",
		"-e", "BAO_CLUSTER_ADDR=https://127.0.0.1:8201", "-e", "BAO_RAFT_NODE_ID=n0",
		smokeOpenBaoImage, "server", "-config=/etc/bao/config.hcl")

	direct := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost"}}}
	do := func(c *http.Client, method, url string, hdr map[string]string, body string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err != nil {
			return 0, err.Error(), nil
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	wait := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(60 * time.Second); !ok(); time.Sleep(400 * time.Millisecond) {
			if time.Now().After(deadline) {
				logs, _ := exec.Command("docker", "logs", "--tail", "25", baoName).CombinedOutput()
				t.Fatalf("timed out waiting for %s\n%s", what, logs)
			}
		}
	}
	wait("OpenBao to answer", func() bool {
		code, _, _ := do(direct, "GET", fmt.Sprintf("https://127.0.0.1:%d/v1/sys/health?uninitcode=200&sealedcode=200", baoPort), nil, "")
		return code == 200
	})
	slow := &http.Client{Timeout: 90 * time.Second, Transport: direct.Transport}
	if code, body, _ := do(slow, "PUT", fmt.Sprintf("https://127.0.0.1:%d/v1/sys/init", baoPort), nil, `{"recovery_shares":1,"recovery_threshold":1}`); code != 200 {
		t.Fatalf("init = %d %s", code, body)
	}
	wait("OpenBao to unseal", func() bool {
		code, _, _ := do(direct, "GET", fmt.Sprintf("https://127.0.0.1:%d/v1/sys/health", baoPort), nil, "")
		return code == 200
	})

	startEnvoy := func(tag, caPEM string, sans []string, sni string) string {
		t.Helper()
		cfgPath := filepath.Join(dir, "envoy-"+tag+".json")
		mustWrite(t, cfgPath, envoyConfig(t, spec, spec.Host(), sni, caPEM, sans), 0o644)
		name := "envoy-pub-" + tag + "-" + sfx
		port := free()
		ip := envoyIP
		if tag != "good" {
			ip = fmt.Sprintf("%s%d", strings.TrimSuffix(subnet, "0/24"), 20+len(containers))
		}
		run(name, "--network", network, "--ip", ip, "-p", fmt.Sprintf("127.0.0.1:%d:18080", port),
			"-v", cfgPath+":/c.json:ro", smokeEnvoyImage, "-c", "/c.json")
		url := fmt.Sprintf("http://127.0.0.1:%d", port)
		wait("Envoy "+tag, func() bool { c, _, _ := do(http.DefaultClient, "GET", url+"/", nil, ""); return c != 0 })
		return url
	}
	edge := startEnvoy("good", caPEM, []string{spec.Host()}, spec.Host())

	reachedOpenBao := func(h http.Header) bool { return h.Get("X-Envoy-Upstream-Service-Time") != "" }
	pub := func(method, path string, hdr map[string]string) (int, string, bool) {
		code, body, h := do(http.DefaultClient, method, edge+path, hdr, "")
		return code, body, reachedOpenBao(h)
	}

	// --- the allowlist, end to end -------------------------------------------
	// Reached: OpenBao answers (403: no token). The answer proves the whole
	// path - the route matched, the TLS was re-encrypted and verified, and
	// OpenBao accepted the forwarded address.
	for _, r := range []struct{ method, path string }{
		{"GET", "/v1/auth/token/lookup-self"},
		{"GET", "/v1/secret/data/tenants/acme/prod/STRIPE_SECRET_KEY"},
		{"GET", "/v1/secret/data/tenants/acme/prod/STRIPE_SECRET_KEY?version=3"},
	} {
		code, body, reached := pub(r.method, r.path, nil)
		if !reached || code != 403 || !strings.Contains(body, "permission denied") {
			t.Errorf("%s %s = %d reached=%v %q, want OpenBao's own 403", r.method, r.path, code, reached, body)
		}
	}
	// A login is routed too. An unknown mount is OpenBao's 4xx, which is the
	// point: the request left Envoy.
	if code, body, reached := pub("PUT", "/v1/auth/jwt-nowhere/login", nil); !reached || code < 400 || code >= 500 {
		t.Errorf("login = %d reached=%v %q, want a 4xx from OpenBao", code, reached, body)
	}
	// Not reached: Envoy answers 404 itself, with nothing from OpenBao.
	for _, r := range []struct{ method, path string }{
		{"GET", "/v1/sys/health"}, {"GET", "/v1/sys/seal-status"}, {"PUT", "/v1/sys/init"}, {"GET", "/v1/sys/metrics"},
		{"GET", "/v1/sys/leader"}, {"LIST", "/v1/secret/metadata/tenants/acme/prod"}, {"GET", "/v1/secret/metadata/tenants/acme/prod/X"},
		{"PUT", "/v1/secret/data/tenants/acme/prod/X"}, {"DELETE", "/v1/secret/data/tenants/acme/prod/X"},
		{"GET", "/v1/secret/data/tenants/acme/prod/"}, {"GET", "/v1/secret/data/tenants/acme/prod/X/y"},
		{"GET", "/v1/secret/data/tenants/acme/prod/a-b"}, {"GET", "/ui/"}, {"GET", "/"},
		{"POST", "/v1/auth/token/create"}, {"GET", "/v1/auth/jwt-nowhere/login"},
		{"GET", "/v1/secret/data/tenants/acme/prod/X%2F..%2Fother"},
	} {
		code, body, reached := pub(r.method, r.path, nil)
		if reached || (code != 404 && code != 400) {
			t.Errorf("%s %s = %d reached=%v %q, want Envoy's 404 (or a refusal), nothing from OpenBao", r.method, r.path, code, reached, body)
		}
	}

	// --- the Gateway's port is not an open door ------------------------------
	// From the host the peer is the bridge, not the proxy: not a trusted range.
	// OpenBao refuses it, with or without a header.
	for name, hdr := range map[string]map[string]string{"no header": nil, "spoofed header": {"X-Forwarded-For": "10.0.0.5"}} {
		code, body, _ := do(direct, "GET", fmt.Sprintf("https://127.0.0.1:%d/v1/auth/token/lookup-self", pubPort), hdr, "")
		if code != 400 {
			t.Errorf("direct to the Gateway's port, %s: %d %q, want 400 (peer not trusted to set X-Forwarded-For)", name, code, body)
		}
	}
	// The in-cluster listener keeps working without any header: ESO, the
	// converge Job and the kubelet reach it directly.
	if code, _, _ := do(direct, "GET", fmt.Sprintf("https://127.0.0.1:%d/v1/sys/health", baoPort), nil, ""); code != 200 {
		t.Errorf("the API listener refuses a plain request: %d", code)
	}

	// --- X-Forwarded-For: the audit log names the client, not the proxy, and a
	// header the client sent changes nothing ----------------------------------
	audited := func() string {
		out, _ := exec.Command("docker", "logs", baoName).CombinedOutput()
		last := ""
		for _, l := range bytes.Split(out, []byte("\n")) {
			var e struct {
				Type    string `json:"type"`
				Request struct {
					Path    string `json:"path"`
					Remote  string `json:"remote_address"`
					Headers map[string]any
				} `json:"request"`
			}
			if json.Unmarshal(l, &e) == nil && e.Type == "request" && e.Request.Path == "auth/token/lookup-self" {
				last = e.Request.Remote
			}
		}
		return last
	}
	pub("GET", "/v1/auth/token/lookup-self", nil)
	var honest string
	wait("the audit entry", func() bool { honest = audited(); return honest != "" })
	pub("GET", "/v1/auth/token/lookup-self", map[string]string{"X-Forwarded-For": "203.0.113.77, 198.51.100.9"})
	time.Sleep(time.Second)
	spoofed := audited()
	if honest == envoyIP {
		t.Errorf("the audit log names the proxy (%s): X-Forwarded-For is not being used", honest)
	}
	if spoofed != honest {
		t.Errorf("a client-sent X-Forwarded-For changed the audited address: honest %q, spoofed %q", honest, spoofed)
	}
	if strings.Contains(spoofed, "203.0.113") || strings.Contains(spoofed, "198.51.100") {
		t.Errorf("the audit log believed a client-sent address: %q", spoofed)
	}

	// --- BackendTLSPolicy: the CA and the name are enforced -------------------
	// An Envoy that trusts the wrong CA, or expects another name, cannot reach
	// OpenBao at all: 503 from Envoy, never a response from OpenBao.
	for tag, cfg := range map[string]struct {
		ca   string
		sans []string
		sni  string
	}{
		"wrongca":   {otherPEM, []string{spec.Host()}, spec.Host()},
		"wrongname": {caPEM, []string{"not-openbao.example.com"}, spec.Host()},
	} {
		url := startEnvoy(tag, cfg.ca, cfg.sans, cfg.sni)
		code, body, h := do(http.DefaultClient, "GET", url+"/v1/auth/token/lookup-self", nil, "")
		if code != 503 || reachedOpenBao(h) {
			t.Errorf("%s: %d %q: an unverifiable backend must not be reached", tag, code, body)
		}
	}
}
