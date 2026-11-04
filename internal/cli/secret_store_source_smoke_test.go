//go:build integration

package cli

// The docker smoke: the CLI's OWN writer path (baoStoreSource) against a real,
// unmodified OpenBao.
//
// The unit tests drive a fake that enforces a copy of the writer policy. This
// proves the copy: it applies the policy and role the OpenBao component's
// converge Job creates (pkg/deploy/openbao, BuildAccess), signs in with a JWT
// the way a minted service-account token would, and runs set / import / list
// through the same code a person's `forge secret` runs. What it settles that
// the fake cannot:
//
//   - the writer role accepts the login the CLI sends (role, audience, subject);
//   - a create-only write is refused with a 400 whose text the CLI recognises as
//     "already exists" (errStoreValueExists), against the real server;
//   - the writer cannot read a value back, and cannot leave its env's directory;
//   - the value reaches the store as a string at the path the policy covers.
//
// Tagged integration: it runs the real docker, which the default build's
// host-tool tripwire (hermetic_host_test.go) refuses in a unit test. Run it:
//
//	go test -tags=integration -count=1 -run TestSmoke_WriterPath -v ./internal/cli/

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const smokeOpenBaoImage = "quay.io/openbao/openbao:2.6.2"
const smokeRootToken = "smoke-root"

type smokeBao struct {
	t    *testing.T
	base string
}

func startSmokeBao(t *testing.T) *smokeBao {
	t.Helper()
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	if exec.Command("docker", "image", "inspect", smokeOpenBaoImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", smokeOpenBaoImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", smokeOpenBaoImage, err, out)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"-p", fmt.Sprintf("127.0.0.1:%d:8200", port),
		"-e", "BAO_DEV_ROOT_TOKEN_ID="+smokeRootToken, "-e", "BAO_DEV_LISTEN_ADDRESS=0.0.0.0:8200",
		smokeOpenBaoImage, "server", "-dev").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", id).Run() })
	b := &smokeBao{t: t, base: fmt.Sprintf("http://127.0.0.1:%d/v1/", port)}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if code, _, _ := b.do(context.Background(), http.MethodGet, "sys/health", nil, smokeRootToken, nil); code == 200 {
			return b
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
			t.Fatalf("openbao not ready:\n%s", logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// do is the baoTransport the production code uses, over plain HTTP to the
// container instead of the API server's service proxy.
func (b *smokeBao) do(ctx context.Context, method, path string, query url.Values, token string, payload any) (int, map[string]any, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	u := b.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

func (b *smokeBao) root(method, path string, payload any) {
	b.t.Helper()
	if code, out, err := b.do(context.Background(), method, path, nil, smokeRootToken, payload); err != nil || code/100 != 2 {
		b.t.Fatalf("%s %s = %d %v (%v)", method, path, code, out, err)
	}
}

// smokeKube hands the CLI a token signed by the issuer the mount trusts.
type smokeKube struct {
	fakeKube
	key *rsa.PrivateKey
	t   *testing.T
}

func (k *smokeKube) MintToken(_ context.Context, ns, sa, aud string, ttl time.Duration) (string, error) {
	if ttl > 10*time.Minute {
		return "", fmt.Errorf("ttl %s exceeds the role's ten minutes", ttl)
	}
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://kubernetes.default.svc", "aud": []string{aud},
		"sub": "system:serviceaccount:" + ns + ":" + sa,
		"iat": now.Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(ttl).Unix(),
	}).SignedString(k.key)
}

func TestSmoke_WriterPathAgainstARealOpenBao(t *testing.T) {
	b := startSmokeBao(t)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	e := storeEnv()
	bao := e.OpenBao
	// The converge Job's side: the self mount, the writer policy, the writer
	// role (pkg/deploy/openbao BuildAccess, restated field for field).
	b.root(http.MethodPut, "sys/auth/"+bao.AuthMount, map[string]any{"type": "jwt"})
	b.root(http.MethodPut, "auth/"+bao.AuthMount+"/config", map[string]any{
		"jwt_validation_pubkeys": []string{pub}, "bound_issuer": "https://kubernetes.default.svc",
	})
	b.root(http.MethodPut, "sys/policies/acl/forge-secret-writer", map[string]any{"policy": `
path "secret/data/tenants/acme/prod/*"     { capabilities = ["create", "update"] }
path "secret/metadata/tenants/acme/prod/*" { capabilities = ["list", "read"] }
path "secret/metadata/tenants/acme/prod"   { capabilities = ["list"] }
`})
	// LITERAL names, not the CLI's constants: this is what pins the CLI to the
	// role and account the component really creates. A rename on either side
	// must fail here, which a shared constant would hide.
	b.root(http.MethodPut, "auth/"+bao.AuthMount+"/role/forge-secret-writer", map[string]any{
		"role_type": "jwt", "user_claim": "sub",
		"bound_audiences": []string{bao.Audience},
		"bound_subject":   "system:serviceaccount:openbao:forge-secret-writer",
		"token_policies":  []string{"forge-secret-writer"},
		"token_type":      "batch", "token_ttl": 600, "token_max_ttl": 600,
	})

	kube := &smokeKube{fakeKube: *newFakeKube(), key: key, t: t}
	src, err := newOpenBaoSource(e, bao, kube, b, "ctx-hub")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// set: create, then rotate.
	if v, err := src.put(ctx, "API_KEY", canary, false); err != nil || v != 1 {
		t.Fatalf("first set = v%d %v", v, err)
	}
	if v, err := src.put(ctx, "API_KEY", canary+"-2", false); err != nil || v != 2 {
		t.Fatalf("rotation = v%d %v, want v2", v, err)
	}

	// import: create-only, against a name that exists and one that does not.
	if _, err := src.put(ctx, "API_KEY", "other", true); err != errStoreValueExists {
		t.Fatalf("create-only over an existing name = %v, want errStoreValueExists", err)
	}
	if v, err := src.put(ctx, "NEW_NAME", "fresh", true); err != nil || v != 1 {
		t.Fatalf("create-only of a new name = v%d %v", v, err)
	}
	// ...and the refused import changed nothing: still v2.
	if got, err := src.presence(ctx, []string{"API_KEY", "NEW_NAME", "MISSING"}); err != nil ||
		got["API_KEY"] != 2 || got["NEW_NAME"] != 1 || len(got) != 2 {
		t.Fatalf("presence = %v %v, want API_KEY v2, NEW_NAME v1, MISSING absent", got, err)
	}

	// The writer cannot read a value back, and cannot leave its env.
	for _, denied := range []struct{ method, path string }{
		{http.MethodGet, "secret/data/tenants/acme/prod/API_KEY"},
		{http.MethodGet, "secret/data/tenants/acme/prod/API_KEY?version=1"},
		{http.MethodDelete, "secret/data/tenants/acme/prod/API_KEY"},
		{http.MethodGet, "secret/data/tenants/acme/other/API_KEY"},
	} {
		code, _, _ := b.do(ctx, denied.method, denied.path, nil, src.token, nil)
		if code != http.StatusForbidden {
			t.Errorf("writer %s %s = %d, want 403", denied.method, denied.path, code)
		}
	}
	if code, _, _ := b.do(ctx, http.MethodPut, "secret/data/tenants/acme/other/X", nil, src.token,
		map[string]any{"data": map[string]any{"value": "x"}}); code != http.StatusForbidden {
		t.Errorf("write outside the env = %d, want 403", code)
	}

	// What the store holds is what was typed, as a string at the covered path.
	code, out, _ := b.do(ctx, http.MethodGet, "secret/data/tenants/acme/prod/API_KEY", nil, smokeRootToken, nil)
	if code != 200 || dig(out, "data", "data", "value") != canary+"-2" {
		t.Errorf("stored value = %d %v", code, out)
	}

	// A token for the wrong audience cannot sign in.
	wrong, _ := newOpenBaoSource(e, bao, &smokeKube{fakeKube: *newFakeKube(), key: key, t: t}, b, "ctx-hub")
	wrong.login.Audience = "https://elsewhere.example.com/c/hub"
	if _, err := wrong.put(ctx, "X", "v", false); err == nil || !strings.Contains(err.Error(), "sign in") {
		t.Errorf("a token for another audience signed in: %v", err)
	}
}
