package openbao

// The docker smoke: the REAL, unmodified OpenBao 2.6.2 (the version control-plane
// runs in prod), started from the config this package RENDERS, with TLS and a
// static seal, and the embedded converge.sh run unchanged inside the pinned
// kubectl image against it.
//
// The unit tests prove the derivation and the objects; this proves the thing
// they are for. It settles, against a live server:
//
//   - converge.sh initialises an empty store, persists the recovery Secret
//     BEFORE it converges anything, applies the derived access document, and
//     revokes the root token it ran as;
//   - the self cluster's JWT mount verifies tokens against a PEM that
//     converge.sh built from the cluster's JWKS with nothing but jq, base64 and
//     xxd (an RSA key and an EC P-256 key, so both DER encoders run);
//   - a rerun converges as the Job's own service-account token, not as root;
//   - a namespace's login reads exactly the names it was granted, and nothing
//     else (a sibling name, the listing, the metadata path);
//   - `widen` grants the union of the old and new shapes, `converge` narrows to
//     the new one, and a binding that left the shape loses its role;
//   - a `delegated` principal can author mounts and env-* policies without
//     being able to read a value, and the secret writer can write only its env.
//
// kubectl is replaced by a ten-line shim over a directory: converge.sh asks it
// four questions (does this object exist, give me this field, create, apply).
//
// Gated by -short (it boots containers). Run it alone:
//
//	go test -count=1 -run TestSmoke -v ./pkg/deploy/openbao/

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/reliant-labs/forge/pkg/secretaccess"
)

const smokeOpenBaoImage = "quay.io/openbao/openbao:2.6.2"

const kubectlShim = `#!/bin/bash
# A directory-backed stand-in for the four kubectl questions converge.sh asks.
# This fake API's state is shared with the host test runner, whose UID need not
# match the container's. Do not inherit the client's credential-protecting 077
# umask: the host must read the objects and remove them (including nested dirs).
# Only disposable fixture data lives here; the real Kubernetes API has no such
# shared-filesystem boundary.
umask 000
S=/state
ns=""; args=()
while [ $# -gt 0 ]; do
  case $1 in -n) ns=$2; shift 2 ;; *) args+=("$1"); shift ;; esac
done
set -- "${args[@]}"
case $1 in
get)
  if [ "$2" = --raw ]; then cat "/raw/$(echo "$3" | tr / _)"; exit 0; fi
  d=$S/$ns/$2-$3
  [ -d "$d" ] || { echo "Error from server (NotFound): $2 \"$3\" not found" >&2; exit 1; }
  case $5 in
  name) echo "$2/$3" ;;
  jsonpath={.metadata.uid}) cat "$d/.uid" ;;
  jsonpath=*) k=${5#jsonpath=\{.data.}; k=${k%\}}; k=${k//\\/}; cat "$d/$k" ;;
  esac ;;
create|apply)
  doc=$(cat)
  kind=$(jq -r .kind <<<"$doc" | tr A-Z a-z); name=$(jq -r .metadata.name <<<"$doc"); dns=$(jq -r .metadata.namespace <<<"$doc")
  d=$S/$dns/$kind-$name
  if [ "$1" = create ] && [ -d "$d" ]; then echo "Error from server (AlreadyExists): $kind \"$name\" already exists" >&2; exit 1; fi
  mkdir -p "$d"
  [ -f "$d/.uid" ] || echo "uid-$RANDOM-$RANDOM-$RANDOM" >"$d/.uid"
  for k in $(jq -r '.data | keys[]' <<<"$doc"); do jq -j --arg k "$k" '.data[$k]' <<<"$doc" >"$d/$k"; done
  echo "$kind/$name configured" ;;
*) echo "unsupported: $*" >&2; exit 2 ;;
esac
`

type smoke struct {
	t        *testing.T
	net      string
	server   string
	port     int
	dir      string
	ca       *x509.CertPool
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
	issuer   string
	spec     Spec
	hexKey   string
	tlsDir   string
	stateDi  string
	tokenKey any
	env      map[string]string
	// dockerArgs are extra `docker run` arguments for the converge container
	// (mounts, hosts).
	dockerArgs []string
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestSmoke_ConvergeAgainstARealOpenBao(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: boots OpenBao and the converge image; runs in task test")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	s := newSmoke(t)

	// ---- 1. a first install: nothing applied yet, so `widen` has nothing to do
	s.start()
	out := s.converge(PhaseWiden, 0)
	mustContain(t, out, "first install")

	// ---- 2. converge: init, persist the recovery Secret, apply, revoke root
	out = s.converge(PhaseConverge, 0)
	for _, want := range []string{"initialising", "persisted to Secret openbao-recovery", "enabling auth mount jwt-local", "recorded the applied access document", "revoked this run's root token"} {
		mustContain(t, out, want)
	}
	if i, a := strings.Index(out, "persisted to Secret"), strings.Index(out, "applied "); !(i >= 0 && a > i) {
		t.Errorf("the recovery Secret must be persisted BEFORE anything is converged:\n%s", out)
	}
	rec := s.stateFile("openbao", "secret-openbao-recovery", "init.json")
	var init struct {
		RootToken string   `json:"root_token"`
		Recovery  []string `json:"recovery_keys"`
	}
	raw, _ := base64.StdEncoding.DecodeString(rec)
	if err := json.Unmarshal(raw, &init); err != nil || init.RootToken == "" || len(init.Recovery) != 5 {
		t.Fatalf("the recovery Secret must hold the init output (5 recovery keys + root token): %v %q", err, raw)
	}
	if code, _ := s.api(init.RootToken, http.MethodGet, "auth/token/lookup-self", nil); code == 200 {
		t.Error("the initial root token must be revoked by the run that created it")
	}
	if s.stateFile("openbao", "configmap-openbao-ca", "ca.crt") == "" {
		t.Error("the CA must be published for the SecretStores")
	}

	// A second init must be impossible: the store, not a lock object, is the lock.
	if code, _ := s.api("", http.MethodPut, "sys/init", map[string]any{"recovery_shares": 1, "recovery_threshold": 1}); code != 400 {
		t.Errorf("re-initialising an initialised store answered %d, want 400", code)
	}

	// ---- 3. values go in through the consumer that was granted write
	cp := s.login("jwt-local", "consumer-control-plane-service", s.audience(), "system:serviceaccount:cp:admin-server", s.rsaKey)
	for _, n := range []string{"A_NAME", "B_NAME", "C_NAME", "OTHER_NAME", "TOKEN"} {
		s.must(cp, http.MethodPut, "secret/data/tenants/demo/prod/"+n, map[string]any{"data": map[string]any{"value": "v-" + n}})
	}
	if code, _ := s.api(cp, http.MethodGet, "secret/data/tenants/demo/prod/A_NAME", nil); code != 403 {
		t.Errorf("the write-only consumer read a value (HTTP %d): a store whose writer can read has no exact-path property to protect", code)
	}

	// ---- 4. a namespace reads exactly its names
	s.expectReads("app", map[string]int{"A_NAME": 200, "B_NAME": 200, "C_NAME": 403, "OTHER_NAME": 403})
	s.expectReads("other", map[string]int{"TOKEN": 200, "A_NAME": 403})
	tok := s.login("jwt-local", s.role("app"), s.audience(), "system:serviceaccount:app:reliant-secrets", s.rsaKey)
	if code, _ := s.api(tok, http.MethodGet, "secret/metadata/tenants/demo/prod/A_NAME", nil); code != 403 {
		t.Errorf("metadata is readable: %d", code)
	}
	if code, _ := s.api(tok, "LIST", "secret/metadata/tenants/demo/prod", nil); code != 403 {
		t.Errorf("the env directory is listable: %d", code)
	}
	if code, _ := s.api(s.badToken(), http.MethodPut, "auth/jwt-local/login", map[string]any{"role": s.role("app"), "jwt": s.sign(s.rsaKey, "wrong-audience", "system:serviceaccount:app:reliant-secrets")}); code == 200 {
		t.Error("a token for another audience logged in")
	}
	if code, _ := s.api("", http.MethodPut, "auth/jwt-local/login", map[string]any{"role": s.role("app"), "jwt": s.sign(s.rsaKey, s.audience(), "system:serviceaccount:intruder:reliant-secrets")}); code == 200 {
		t.Error("a token for another namespace logged in with this namespace's role")
	}

	// ---- 5. the writer writes its env only; the delegated principal authors
	// access and cannot read a value
	w := s.login("jwt-local", RoleWriter, s.audience(), "system:serviceaccount:openbao:forge-secret-writer", s.rsaKey)
	s.must(w, http.MethodPut, "secret/data/tenants/demo/prod/WRITTEN", map[string]any{"data": map[string]any{"value": "x"}})
	if code, _ := s.api(w, http.MethodPut, "secret/data/tenants/demo/other-env/X", map[string]any{"data": map[string]any{"value": "x"}}); code != 403 {
		t.Errorf("the writer wrote outside its env: %d", code)
	}
	if code, _ := s.api(w, http.MethodGet, "secret/data/tenants/demo/prod/WRITTEN", nil); code != 403 {
		t.Errorf("the writer can read a value: %d", code)
	}
	adm := s.login("jwt-local", "consumer-access-admin", s.audience(), "system:serviceaccount:cp:workspace-controller", s.rsaKey)
	s.must(adm, http.MethodPost, "sys/auth/jwt-delegated-x", map[string]any{"type": "jwt"})
	s.must(adm, http.MethodPut, "sys/policies/acl/env-x", map[string]any{"policy": `path "secret/data/x" { capabilities = ["read"] }`})
	if code, _ := s.api(adm, http.MethodPut, "sys/policies/acl/not-env-scoped", map[string]any{"policy": `path "secret/data/*" { capabilities = ["read"] }`}); code != 403 {
		t.Errorf("a delegated principal wrote a policy outside env-*: %d", code)
	}
	if code, _ := s.api(adm, http.MethodGet, "secret/data/tenants/demo/prod/A_NAME", nil); code != 403 {
		t.Errorf("a delegated principal can read a value: %d", code)
	}
	if code, _ := s.api(adm, http.MethodPost, "sys/auth/jwt-delegated-x/../../sys/mounts/evil", map[string]any{"type": "kv"}); code/100 == 2 {
		t.Errorf("a delegated principal mounted a secrets engine: %d", code)
	}

	// ---- 6. a rerun converges as the Job's own token, and changes nothing
	out = s.converge(PhaseConverge, 0)
	mustContain(t, out, "logged in as openbao-converge on jwt-local")
	if strings.Contains(out, "initialising") || strings.Contains(out, "root token") {
		t.Errorf("a rerun must not initialise or use root:\n%s", out)
	}
	s.expectReads("app", map[string]int{"A_NAME": 200, "B_NAME": 200, "C_NAME": 403})

	// ---- 7. a promotion: B stays, C arrives, A leaves, and a namespace goes
	oldDoc := s.doc()
	next := s.spec
	next.Access.Bindings = []Binding{{Cluster: "local", Namespace: "app", Names: []string{"B_NAME", "C_NAME"}}}
	s.setSpec(next)
	out = s.converge(PhaseWiden, 0)
	mustContain(t, out, "widened")
	s.expectReads("app", map[string]int{"A_NAME": 200, "B_NAME": 200, "C_NAME": 200, "OTHER_NAME": 403})
	s.expectReads("other", map[string]int{"TOKEN": 200}) // a rollout still needs its old grant

	out = s.converge(PhaseConverge, 0)
	mustContain(t, out, "removing role jwt-local/"+s.role("other"))
	s.expectReads("app", map[string]int{"A_NAME": 403, "B_NAME": 200, "C_NAME": 200})
	if code, _ := s.api("", http.MethodPut, "auth/jwt-local/login", map[string]any{"role": s.role("other"), "jwt": s.sign(s.rsaKey, s.audience(), "system:serviceaccount:other:reliant-secrets")}); code == 200 {
		t.Error("a namespace that left the shape can still log in")
	}
	gone := Removed(oldDoc, s.doc())
	if len(gone.Roles) != 1 || len(gone.Policies) != 1 || len(gone.Mounts) != 0 {
		t.Errorf("planner says %d roles %d policies %d mounts left, want 1/1/0", len(gone.Roles), len(gone.Policies), len(gone.Mounts))
	}

	// ---- 8. the EC key verifies too (the second DER encoder), as the Job's token
	s.tokenKey = s.ecKey
	out = s.converge(PhaseConverge, 0)
	mustContain(t, out, "logged in as openbao-converge")
	if code, _ := s.api("", http.MethodPut, "auth/jwt-local/login", map[string]any{"role": s.role("app"), "jwt": s.sign(s.ecKey, s.audience(), "system:serviceaccount:app:reliant-secrets")}); code != 200 {
		t.Errorf("an ES256 token from the cluster's EC key did not log in: %d", code)
	}

	// ---- 9. a store that lost its volume is refused, never re-initialised
	s.stop()
	s.start()
	out = s.converge(PhaseConverge, exitRefused)
	mustContain(t, out, "Initialising would strand the recovery material")
	if code, _ := s.api("", http.MethodGet, "sys/init", nil); code != 200 {
		t.Fatal(code)
	}
}

func mustContain(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Fatalf("output lacks %q:\n%s", want, out)
	}
}

// ---- fixture ---------------------------------------------------------------

func newSmoke(t *testing.T) *smoke {
	t.Helper()
	for _, img := range []string{smokeOpenBaoImage, DefaultKubectlImage} {
		if exec.Command("docker", "image", "inspect", img).Run() != nil {
			if out, err := exec.Command("docker", "pull", img).CombinedOutput(); err != nil {
				t.Skipf("cannot pull %s: %v\n%s", img, err, out)
			}
		}
	}
	s := &smoke{t: t, dir: t.TempDir(), issuer: "https://kubernetes.default.svc"}
	var err error
	if s.rsaKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if s.ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	s.tokenKey = s.rsaKey
	sfx := fmt.Sprintf("%d", time.Now().UnixNano()%1e9)
	s.net = "bao-smoke-" + sfx
	s.server = "bao-server-" + sfx
	if out, err := exec.Command("docker", "network", "create", s.net).CombinedOutput(); err != nil {
		t.Fatalf("docker network: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-fv", s.server).Run()
		_ = exec.Command("docker", "network", "rm", s.net).Run()
	})

	s.tlsDir = filepath.Join(s.dir, "tls")
	s.writeCerts()
	kb := make([]byte, 32)
	_, _ = rand.Read(kb)
	s.hexKey = hex.EncodeToString(kb)

	s.stateDi = filepath.Join(s.dir, "state")
	for _, d := range []string{s.stateDi, filepath.Join(s.dir, "raw"), filepath.Join(s.dir, "bin")} {
		mustMkdir(t, d)
	}
	_ = os.Chmod(s.stateDi, 0o777)
	mustWrite(t, filepath.Join(s.dir, "bin", "kubectl"), kubectlShim, 0o755)
	s.writeJWKS()

	spec := raftSpec()
	spec.Replicas = 1
	spec.Access.SelfCluster = "local"
	spec.Access.Clusters = []AccessCluster{{ID: "local", Audience: "https://" + s.server + ":8200/c/local"}}
	spec.Access.Bindings = []Binding{
		{Cluster: "local", Namespace: "app", Names: []string{"A_NAME", "B_NAME"}},
		{Cluster: "local", Namespace: "other", Names: []string{"TOKEN"}},
	}
	spec.Consumers = []Consumer{
		{Name: "control-plane-service", Workload: "admin-server", Namespace: "cp", Grants: []Grant{{Path: "secret/data/*", Capabilities: []string{"create", "update"}}}},
		{Name: "access-admin", Workload: "workspace-controller", Namespace: "cp", Delegated: true},
	}
	s.setSpec(spec)
	return s
}

func (s *smoke) audience() string { return s.spec.Access.Clusters[0].Audience }

func (s *smoke) role(ns string) string {
	return secretaccess.Scope{Org: testOrg, Env: testEnv}.PolicyName("local", ns)
}

func (s *smoke) doc() AccessDoc {
	d, err := BuildAccess(s.spec)
	if err != nil {
		s.t.Fatal(err)
	}
	return d
}

// setSpec renders the spec and installs the artifacts the Job would mount: the
// embedded script and the access document, exactly as rendered.
func (s *smoke) setSpec(spec Spec) {
	s.t.Helper()
	s.spec = spec
	objs, err := Render(spec)
	if err != nil {
		s.t.Fatal(err)
	}
	cmDir := filepath.Join(s.dir, "converge")
	mustMkdir(s.t, cmDir)
	var found bool
	for _, o := range objs {
		if o.GetKind() != "ConfigMap" {
			continue
		}
		data, _ := o.Object["data"].(map[string]any)
		if _, ok := data["access.json"]; !ok {
			continue
		}
		for k, v := range data {
			mustWrite(s.t, filepath.Join(cmDir, k), v.(string), 0o644)
		}
		found = true
	}
	if !found {
		s.t.Fatal("no converge ConfigMap rendered")
	}
	for _, o := range objs {
		if o.GetKind() == "Job" && o.GetLabels()[LabelJobName] == "openbao-converge" {
			s.env = containerEnv(o.Object)
		}
	}
	for _, o := range objs {
		if o.GetKind() == "ConfigMap" && o.GetName() == "openbao-config" {
			data, _ := o.Object["data"].(map[string]any)
			mustWrite(s.t, filepath.Join(s.dir, "config.hcl"), data["config.hcl"].(string), 0o644)
		}
	}
}

func containerEnv(job map[string]any) map[string]string {
	out := map[string]string{}
	spec, _ := job["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	ps, _ := tmpl["spec"].(map[string]any)
	cs, _ := ps["containers"].([]any)
	c, _ := cs[0].(map[string]any)
	envs, _ := c["env"].([]any)
	for _, e := range envs {
		m := e.(map[string]any)
		if v, ok := m["value"].(string); ok {
			out[m["name"].(string)] = v
		}
	}
	return out
}

func (s *smoke) writeCerts() {
	s.t.Helper()
	mustMkdir(s.t, s.tlsDir)
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smoke-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		s.t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: s.server}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		DNSNames: []string{s.server, "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, ca, &srvKey.PublicKey, caKey)
	if err != nil {
		s.t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(srvKey)
	enc := func(typ string, der []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
	}
	mustWrite(s.t, filepath.Join(s.tlsDir, "tls.crt"), enc("CERTIFICATE", srvDER), 0o644)
	mustWrite(s.t, filepath.Join(s.tlsDir, "tls.key"), enc("EC PRIVATE KEY", keyDER), 0o644)
	mustWrite(s.t, filepath.Join(s.tlsDir, "ca.crt"), enc("CERTIFICATE", caDER), 0o644)
	s.ca = x509.NewCertPool()
	s.ca.AddCert(ca)
}

func (s *smoke) writeJWKS() {
	s.t.Helper()
	rawDir := filepath.Join(s.dir, "raw")
	pub := s.rsaKey.PublicKey
	ecx := s.ecKey.PublicKey
	pad := func(b *big.Int) []byte { return b.FillBytes(make([]byte, 32)) }
	jwks := map[string]any{"keys": []any{
		map[string]any{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "rsa", "n": b64u(pub.N.Bytes()), "e": b64u(big.NewInt(int64(pub.E)).Bytes())},
		map[string]any{"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "ec", "x": b64u(pad(ecx.X)), "y": b64u(pad(ecx.Y))},
	}}
	j, _ := json.Marshal(jwks)
	mustWrite(s.t, filepath.Join(rawDir, "_openid_v1_jwks"), string(j), 0o644)
	mustWrite(s.t, filepath.Join(rawDir, "_.well-known_openid-configuration"), `{"issuer":"`+s.issuer+`"}`, 0o644)
}

func (s *smoke) sign(key any, aud, sub string) string {
	s.t.Helper()
	now := time.Now()
	method := jwt.SigningMethod(jwt.SigningMethodRS256)
	if _, ok := key.(*ecdsa.PrivateKey); ok {
		method = jwt.SigningMethodES256
	}
	tok, err := jwt.NewWithClaims(method, jwt.MapClaims{
		"iss": s.issuer, "aud": []string{aud}, "sub": sub,
		"iat": now.Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	}).SignedString(key)
	if err != nil {
		s.t.Fatal(err)
	}
	return tok
}

func (s *smoke) badToken() string { return "" }

// ---- containers ------------------------------------------------------------

func (s *smoke) start() {
	s.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	s.port = l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	out, err := exec.Command("docker", "run", "-d", "--name", s.server, "--network", s.net,
		"-p", fmt.Sprintf("127.0.0.1:%d:8200", s.port),
		"--tmpfs", "/openbao/data:uid=100,gid=1000", "--tmpfs", "/openbao/audit:uid=100,gid=1000",
		"-v", s.tlsDir+":/openbao/tls:ro", "-v", filepath.Join(s.dir, "config.hcl")+":/etc/bao/config.hcl:ro",
		"-e", "BAO_STATIC_KEY="+s.hexKey, "-e", "BAO_API_ADDR=https://"+s.server+":8200",
		"-e", "BAO_CLUSTER_ADDR=https://127.0.0.1:8201", "-e", "BAO_RAFT_NODE_ID=n0",
		smokeOpenBaoImage, "server", "-config=/etc/bao/config.hcl").CombinedOutput()
	if err != nil {
		s.t.Fatalf("docker run openbao: %v\n%s", err, out)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if code, _ := s.api("", http.MethodGet, "sys/health?uninitcode=200&sealedcode=200", nil); code == 200 {
			return
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", s.server).CombinedOutput()
			s.t.Fatalf("openbao not ready:\n%s", logs)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (s *smoke) stop() { _ = exec.Command("docker", "rm", "-fv", s.server).Run() }

// converge runs the Job's script in the Job's image, with the Job's rendered
// environment, and returns its combined log. wantExit is the expected status.
func (s *smoke) converge(phase string, wantExit int) string {
	s.t.Helper()
	tokenFile := filepath.Join(s.dir, "token")
	mustWrite(s.t, tokenFile, s.sign(s.tokenKey, ConvergeAudience, "system:serviceaccount:openbao:openbao-converge"), 0o644)
	env := map[string]string{}
	for k, v := range s.env {
		env[k] = v
	}
	env["OPENBAO_PHASE"] = phase
	env["OPENBAO_ADDR"] = "https://" + s.server + ":8200"
	env["OPENBAO_INIT_ADDR"] = "https://" + s.server + ":8200"
	env["OPENBAO_CA_FILE"] = "/tls/ca.crt"
	env["OPENBAO_WAIT_SECONDS"] = "60"
	env["OPENBAO_REJOIN_SECONDS"] = "5"
	env["PATH"] = "/fakebin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	args := []string{"run", "--rm", "--network", s.net, "--user", "65532:65532", "--read-only", "--tmpfs", "/tmp",
		"-v", filepath.Join(s.dir, "converge") + ":/converge:ro", "-v", s.tlsDir + ":/tls:ro",
		"-v", tokenFile + ":/var/run/openbao/token:ro", "-v", filepath.Join(s.dir, "bin") + ":/fakebin:ro",
		"-v", filepath.Join(s.dir, "raw") + ":/raw:ro", "-v", s.stateDi + ":/state",
		"--entrypoint", "bash"}
	args = append(args, s.dockerArgs...)
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, DefaultKubectlImage, "/converge/converge.sh")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		s.t.Fatalf("docker run converge: %v\n%s", err, buf.String())
	}
	if code != wantExit {
		s.t.Fatalf("converge %s exited %d, want %d:\n%s", phase, code, wantExit, buf.String())
	}
	return buf.String()
}

func (s *smoke) stateFile(ns, obj, key string) string {
	b, err := os.ReadFile(filepath.Join(s.stateDi, ns, obj, key))
	if err != nil {
		return ""
	}
	return string(b)
}

// ---- API -------------------------------------------------------------------

func (s *smoke) api(token, method, path string, payload any) (int, map[string]any) {
	s.t.Helper()
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := method
	q := ""
	if method == "LIST" {
		m, q = http.MethodGet, "?list=true"
	}
	req, err := http.NewRequestWithContext(ctx, m, fmt.Sprintf("https://127.0.0.1:%d/v1/%s%s", s.port, path, q), body)
	if err != nil {
		s.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: s.ca, ServerName: s.server}}}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (s *smoke) must(token, method, path string, payload any) {
	s.t.Helper()
	if code, out := s.api(token, method, path, payload); code/100 != 2 {
		s.t.Fatalf("%s %s = %d: %v", method, path, code, out)
	}
}

func (s *smoke) login(mount, role, aud, sub string, key any) string {
	s.t.Helper()
	code, out := s.api("", http.MethodPut, "auth/"+mount+"/login", map[string]any{"role": role, "jwt": s.sign(key, aud, sub)})
	if code != 200 {
		s.t.Fatalf("login %s %s = %d: %v", mount, role, code, out)
	}
	return out["auth"].(map[string]any)["client_token"].(string)
}

func (s *smoke) expectReads(ns string, want map[string]int) {
	s.t.Helper()
	tok := s.login("jwt-local", s.role(ns), s.audience(), "system:serviceaccount:"+ns+":reliant-secrets", s.rsaKey)
	if !strings.HasPrefix(tok, "b.") {
		s.t.Errorf("%s: token %.4s... is not a batch token", ns, tok)
	}
	for name, code := range want {
		if got, _ := s.api(tok, http.MethodGet, "secret/data/tenants/demo/prod/"+name, nil); got != code {
			s.t.Errorf("namespace %s reading %s = %d, want %d", ns, name, got, code)
		}
	}
}

func mustMkdir(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
