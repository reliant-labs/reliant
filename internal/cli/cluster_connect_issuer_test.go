package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

const (
	testIssuerURL    = "https://oidc.acme.example/clusters/vke"
	testGKEIssuerURL = "https://container.googleapis.com/v1/projects/acme/locations/us-central1/clusters/prod"
)

// testECJWK is a public P-256 JWK. kid names it; the key is fresh each call.
func testECJWK(t *testing.T, kid string) map[string]any {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := k.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		t.Fatal(err)
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	return map[string]any{"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid, "x": b64(pt[1:33]), "y": b64(pt[33:])}
}

func jwksJSON(t *testing.T, keys ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// apiPaths is a fake API server's non-resource paths.
type apiPaths struct {
	issuer, jwks string
	err          error
	gets         []string
}

// stubIssuer serves the cluster's discovery document and key set through
// connectAPIGet, and keeps the issuer record in a temp dir.
func stubIssuer(t *testing.T, issuer, jwks string) *apiPaths {
	t.Helper()
	a := &apiPaths{issuer: issuer, jwks: jwks}
	rg, rd := connectAPIGet, connectStateDir
	dir := t.TempDir()
	t.Cleanup(func() { connectAPIGet, connectStateDir = rg, rd })
	connectStateDir = func() string { return dir }
	connectAPIGet = func(_ context.Context, _, path string) ([]byte, error) {
		a.gets = append(a.gets, path)
		if a.err != nil {
			return nil, a.err
		}
		switch path {
		case oidcDiscoveryPath:
			return []byte(fmt.Sprintf(`{"issuer":%q,"jwks_uri":"https://x/openid/v1/jwks"}`, a.issuer)), nil
		case oidcJWKSPath:
			return []byte(a.jwks), nil
		}
		return nil, fmt.Errorf("unexpected path %s", path)
	}
	return a
}

func connectWith(t *testing.T, s *connectStub, kctx, issuer string, extra func(*clusterConnectOptions)) (string, error) {
	t.Helper()
	var out strings.Builder
	opts := clusterConnectOptions{Name: "vke", KubeContext: kctx, Env: "prod", Issuer: issuer, Out: &out}
	if extra != nil {
		extra(&opts)
	}
	err := runClusterConnect(context.Background(), opts)
	return out.String(), err
}

func vkeStub(t *testing.T) *connectStub {
	t.Helper()
	return stubConnect(t, "vke-prod", strings.ReplaceAll(scopeStream(), testCtx, "vke-prod"))
}

func sentIssuer(t *testing.T, s *connectStub) map[string]any {
	t.Helper()
	m, _ := s.caller.requests[procConnectCluster]["issuer"].(map[string]any)
	return m
}

// ── what is sent ────────────────────────────────────────────────────────────

func TestConnectIssuer_AutoUploadsTheClusterKeysOffGKE(t *testing.T) {
	s := vkeStub(t)
	k1, k2 := testECJWK(t, "k1"), testECJWK(t, "k2")
	stubIssuer(t, testIssuerURL, jwksJSON(t, k1, k2))
	out, err := connectWith(t, s, "vke-prod", "", nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	iss := sentIssuer(t, s)
	if iss["issuer"] != testIssuerURL {
		t.Fatalf("issuer = %v, want %s", iss["issuer"], testIssuerURL)
	}
	if _, ok := iss["discover"]; ok {
		t.Errorf("an upload also set discover: %v", iss)
	}
	var set struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal([]byte(iss["jwks"].(string)), &set); err != nil || len(set.Keys) != 2 {
		t.Fatalf("jwks = %v (%v), want both keys", iss["jwks"], err)
	}
	for _, k := range set.Keys {
		if _, ok := k["d"]; ok {
			t.Errorf("a private member was sent: %v", k)
		}
	}
	for _, want := range []string{"issuer    " + testIssuerURL, "2 key(s) uploaded", "store id  vke   (auth mount jwt-vke)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output omits %q:\n%s", want, out)
		}
	}
}

func TestConnectIssuer_AutoDiscoversOnGKEAndReadsNoKeys(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	a := stubIssuer(t, testGKEIssuerURL, jwksJSON(t, testECJWK(t, "k1")))
	out, err := connectWith(t, s, testCtx, "", nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	iss := sentIssuer(t, s)
	if iss["issuer"] != testGKEIssuerURL || iss["discover"] != true {
		t.Fatalf("issuer = %v, want discovery of %s", iss, testGKEIssuerURL)
	}
	if _, ok := iss["jwks"]; ok {
		t.Errorf("discovery also uploaded keys: %v", iss)
	}
	for _, p := range a.gets {
		if p == oidcJWKSPath {
			t.Error("discovery read the key set; the store fetches it itself")
		}
	}
}

func TestConnectIssuer_UploadCanBeForcedOnGKE(t *testing.T) {
	s := stubConnect(t, testCtx, scopeStream())
	stubIssuer(t, testGKEIssuerURL, jwksJSON(t, testECJWK(t, "k1")))
	if out, err := connectWith(t, s, testCtx, issuerUpload, nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if iss := sentIssuer(t, s); iss["jwks"] == nil || iss["discover"] != nil {
		t.Fatalf("issuer = %v, want an upload", iss)
	}
}

func TestConnectIssuer_SkipLeavesTheRegistrationAloneAndWithdrawRemovesIt(t *testing.T) {
	s := vkeStub(t)
	a := stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "k1")))
	if out, err := connectWith(t, s, "vke-prod", issuerSkip, nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, present := s.caller.requests[procConnectCluster]["issuer"]; present {
		t.Error("--issuer skip sent an issuer: an absent one is what leaves a registration alone")
	}
	if len(a.gets) != 0 {
		t.Errorf("--issuer skip read the cluster: %v", a.gets)
	}

	if out, err := connectWith(t, s, "vke-prod", issuerWithdraw, nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	iss, present := s.caller.requests[procConnectCluster]["issuer"].(map[string]any)
	if !present || len(iss) != 0 {
		t.Errorf("--issuer withdraw sent %v, want a present, empty issuer", s.caller.requests[procConnectCluster]["issuer"])
	}
}

// ── what comes back is kept, and the id is the store's ─────────────────────

func TestConnectIssuer_UsesTheStoreClusterIDNotTheName(t *testing.T) {
	s := vkeStub(t)
	s.caller.storeClusterID = "vke-4f3c"
	stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "k1")))
	out, err := connectWith(t, s, "vke-prod", "", nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rec := readConnectIssuerRecord("vke")
	if rec == nil || rec.StoreClusterID != "vke-4f3c" || rec.Issuer != testIssuerURL || rec.Mode != issuerUpload || rec.KeyCount != 1 {
		t.Fatalf("record = %+v", rec)
	}
	for _, want := range []string{"store id  vke-4f3c   (auth mount jwt-vke-4f3c)", `knows this cluster as "vke-4f3c", not "vke"`, "connected_cluster"} {
		if !strings.Contains(out, want) {
			t.Errorf("output omits %q:\n%s", want, out)
		}
	}
	if strings.Contains(readIssuerRecord(t, connectIssuerStatePath("vke")), "BEGIN") {
		t.Error("the record holds key material")
	}
}

func TestConnectIssuer_RerunFollowsKeyRotation(t *testing.T) {
	s := vkeStub(t)
	a := stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "old")))
	if out, err := connectWith(t, s, "vke-prod", "", nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	first := readConnectIssuerRecord("vke")
	firstJWKS := sentIssuer(t, s)["jwks"]

	// Unchanged: the same set, re-sent, and said to be unchanged.
	out, err := connectWith(t, s, "vke-prod", "", nil)
	if err != nil || !strings.Contains(out, "unchanged since the last connect") {
		t.Fatalf("unchanged re-run: %v\n%s", err, out)
	}
	if sentIssuer(t, s)["jwks"] != firstJWKS {
		t.Error("the same key set was not the same bytes")
	}

	// Rotated: the new key is sent and the old one is gone from the request.
	a.jwks = jwksJSON(t, testECJWK(t, "new"))
	out, err = connectWith(t, s, "vke-prod", "", nil)
	if err != nil || !strings.Contains(out, "REPLACED the previous set of 1") {
		t.Fatalf("rotated re-run: %v\n%s", err, out)
	}
	sent := sentIssuer(t, s)["jwks"].(string)
	if !strings.Contains(sent, `"new"`) || strings.Contains(sent, `"old"`) {
		t.Errorf("the rotated set was not what was sent: %s", sent)
	}
	if rec := readConnectIssuerRecord("vke"); rec.KeyFingerprint == first.KeyFingerprint || rec.KeyFingerprint == "" {
		t.Errorf("fingerprint did not move on rotation: %q -> %q", first.KeyFingerprint, rec.KeyFingerprint)
	}
	if s.caller.connects != 3 {
		t.Errorf("connects = %d, want 3", s.caller.connects)
	}
}

func TestConnectIssuer_WithdrawAndDisconnectRemoveTheRecord(t *testing.T) {
	s := vkeStub(t)
	stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "k1")))
	if out, err := connectWith(t, s, "vke-prod", "", nil); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if readConnectIssuerRecord("vke") == nil {
		t.Fatal("no record after a registration")
	}
	if out, err := connectWith(t, s, "vke-prod", issuerWithdraw, nil); err != nil || readConnectIssuerRecord("vke") != nil {
		t.Fatalf("withdraw: %v, record=%v\n%s", err, readConnectIssuerRecord("vke"), out)
	}
	if out, err := connectWith(t, s, "vke-prod", "", nil); err != nil || readConnectIssuerRecord("vke") == nil {
		t.Fatalf("re-register: %v\n%s", err, out)
	}
	s.caller.clusters = []wireConnectedCluster{{ID: "cl_new", Name: "vke"}}
	var dout strings.Builder
	if err := runClusterDisconnect(context.Background(), "vke", "", "prod", "", &dout); err != nil {
		t.Fatal(err)
	}
	if readConnectIssuerRecord("vke") != nil {
		t.Error("disconnect left the issuer record behind")
	}
}

// ── the failure policy ──────────────────────────────────────────────────────

func TestConnectIssuer_AutoCarriesOnWhenTheClusterPublishesNothing(t *testing.T) {
	s := vkeStub(t)
	a := stubIssuer(t, testIssuerURL, "")
	a.err = errors.New("the server could not find the requested resource")
	out, err := connectWith(t, s, "vke-prod", "", nil)
	if err != nil {
		t.Fatalf("auto failed the connect over the issuer: %v\n%s", err, out)
	}
	if _, present := s.caller.requests[procConnectCluster]["issuer"]; present {
		t.Error("an issuer was sent though none could be read")
	}
	for _, want := range []string{"NOT registered", "could not be read", "Deploys are unaffected"} {
		if !strings.Contains(out, want) {
			t.Errorf("output omits %q:\n%s", want, out)
		}
	}
	if s.caller.connects != 1 {
		t.Errorf("connects = %d", s.caller.connects)
	}
}

func TestConnectIssuer_ExplicitModesFailBeforeAnythingIsWritten(t *testing.T) {
	for _, mode := range []string{issuerUpload, issuerDiscover} {
		t.Run(mode, func(t *testing.T) {
			s := vkeStub(t)
			a := stubIssuer(t, testIssuerURL, "")
			a.err = errors.New("forbidden: User cannot get path /openid/v1/jwks")
			out, err := connectWith(t, s, "vke-prod", mode, nil)
			if err == nil || !strings.Contains(err.Error(), "get") {
				t.Fatalf("err = %v, want the read failure\n%s", err, out)
			}
			if len(s.applied) != 0 || s.caller.connects != 0 {
				t.Errorf("a failed explicit --issuer %s still applied %d stage(s) and connected %d time(s)", mode, len(s.applied), s.caller.connects)
			}
		})
	}
}

func TestConnectIssuer_APrivateKeyIsNeverSent(t *testing.T) {
	leak := testECJWK(t, "leaky")
	leak["d"] = "c2VjcmV0LXNjYWxhcg"
	for _, mode := range []string{"", issuerUpload} {
		t.Run("mode="+mode, func(t *testing.T) {
			s := vkeStub(t)
			stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "ok"), leak))
			out, err := connectWith(t, s, "vke-prod", mode, nil)
			if mode == issuerUpload && (err == nil || !strings.Contains(err.Error(), `private or symmetric member "d"`)) {
				t.Fatalf("explicit upload: err = %v", err)
			}
			if mode == "" && err != nil {
				t.Fatalf("auto: %v\n%s", err, out)
			}
			if b, _ := json.Marshal(s.caller.requests); strings.Contains(string(b), "c2VjcmV0LXNjYWxhcg") {
				t.Fatalf("the private scalar reached the control plane: %s", b)
			}
			if strings.Contains(out, "c2VjcmV0LXNjYWxhcg") {
				t.Fatalf("the private scalar was printed:\n%s", out)
			}
		})
	}
}

func TestConnectIssuer_AutoRetriesWithoutTheIssuerWhenTheControlPlaneRefusesIt(t *testing.T) {
	refusal := &cloud.Error{HTTPStatus: http.StatusBadRequest, Code: cloud.CodeInvalidArgument,
		Message: "the issuer host \"kubernetes.default.svc.cluster.local\" is a loopback, private or link-local address", Procedure: procConnectCluster}
	s := vkeStub(t)
	s.caller.refuseIssuer = refusal
	stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "k1")))
	out, err := connectWith(t, s, "vke-prod", "", nil)
	if err != nil {
		t.Fatalf("auto failed the connect over a refused issuer: %v\n%s", err, out)
	}
	if s.caller.connects != 2 {
		t.Fatalf("connects = %d, want the refused one and the retry", s.caller.connects)
	}
	if _, present := s.caller.requests[procConnectCluster]["issuer"]; present {
		t.Error("the retry still carried the issuer")
	}
	if !strings.Contains(out, "the control plane declined it") || !strings.Contains(out, "loopback, private or link-local") {
		t.Errorf("the refusal's reason is not shown:\n%s", out)
	}
	if readConnectIssuerRecord("vke") != nil {
		t.Error("a record was kept for a registration the control plane refused")
	}

	// Explicit: the same refusal is the command's failure.
	s2 := vkeStub(t)
	s2.caller.refuseIssuer = refusal
	stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "k1")))
	if _, err := connectWith(t, s2, "vke-prod", issuerUpload, nil); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("explicit upload: err = %v, want the refusal", err)
	}
	if s2.caller.connects != 1 {
		t.Errorf("explicit upload retried (%d connects)", s2.caller.connects)
	}
}

func TestConnectIssuer_K3sDefaultIssuerGetsAnActionableHint(t *testing.T) {
	refusal := &cloud.Error{HTTPStatus: http.StatusBadRequest, Code: cloud.CodeInvalidArgument, Message: "the issuer host is a loopback, private or link-local address"}
	s := vkeStub(t)
	s.caller.refuseIssuer = refusal
	stubIssuer(t, "https://kubernetes.default.svc.cluster.local", jwksJSON(t, testECJWK(t, "k1")))
	out, err := connectWith(t, s, "vke-prod", "", nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"service-account-issuer=https://kubernetes.default.svc", "@server:*"} {
		if !strings.Contains(out, want) {
			t.Errorf("output omits %q:\n%s", want, out)
		}
	}
	// A public issuer's refusal gets no k3s advice.
	if h := issuerHint(testIssuerURL); h != "" {
		t.Errorf("a public issuer got a hint: %s", h)
	}
}

func TestConnectIssuer_AnAuthFailureIsNotAnIssuerRefusal(t *testing.T) {
	s := vkeStub(t)
	s.caller.refuseIssuer = &cloud.Error{HTTPStatus: http.StatusUnauthorized, Code: cloud.CodeUnauthenticated}
	stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "k1")))
	if _, err := connectWith(t, s, "vke-prod", "", nil); err == nil {
		t.Fatal("a rejected credential was swallowed as an issuer refusal")
	}
	if s.caller.connects != 1 {
		t.Errorf("connects = %d: an auth failure must not be retried", s.caller.connects)
	}
}

func TestParseIssuerMode(t *testing.T) {
	for in, want := range map[string]string{"": "auto", "AUTO": "auto", " upload ": "upload", "discover": "discover", "skip": "skip", "withdraw": "withdraw"} {
		if got, err := parseIssuerMode(in); err != nil || got != want {
			t.Errorf("parseIssuerMode(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := parseIssuerMode("jwks"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

// ── the key set ─────────────────────────────────────────────────────────────

func TestNormalizeJWKS_StableUnderReorderAndRefusesPrivateMembers(t *testing.T) {
	a, b := testECJWK(t, "a"), testECJWK(t, "b")
	j1, n1, f1, err := normalizeJWKS([]byte(jwksJSON(t, a, b)))
	if err != nil || n1 != 2 {
		t.Fatal(n1, err)
	}
	j2, _, f2, err := normalizeJWKS([]byte(jwksJSON(t, b, a)))
	if err != nil || j1 != j2 || f1 != f2 {
		t.Errorf("the same keys in another order are not one value:\n%s\n%s", j1, j2)
	}
	for _, m := range privateJWKMembers {
		leaky := testECJWK(t, "x")
		leaky[m] = "AQAB"
		if _, _, _, err := normalizeJWKS([]byte(jwksJSON(t, a, leaky))); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", m)) {
			t.Errorf("member %q: err = %v, want a refusal naming it", m, err)
		}
	}
	for name, in := range map[string]string{"not json": "<html>", "no keys": `{"keys":[]}`, "no kty": `{"keys":[{"kid":"x"}]}`} {
		if _, _, _, err := normalizeJWKS([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ── the grant ───────────────────────────────────────────────────────────────

func jwksRules(objs []rbacObject) (roles []string) {
	for _, o := range objs {
		if o.Kind != "ClusterRole" && o.Kind != "Role" {
			continue
		}
		rules, _ := o.Body["rules"].([]any)
		for _, r := range rules {
			rule, _ := r.(map[string]any)
			if urls := stringsOf(rule["nonResourceURLs"]); len(urls) > 0 {
				roles = append(roles, o.Kind+" "+o.Name+": "+strings.Join(urls, ",")+" "+strings.Join(stringsOf(rule["verbs"]), ","))
			}
		}
	}
	sort.Strings(roles)
	return roles
}

func TestGrant_JWKSReadIsAClusterRoleRuleInEveryScope(t *testing.T) {
	for name, scope := range map[string]applyScope{"cluster": {Cluster: true}, "namespace": {Namespaces: []string{"acme-prod"}}} {
		t.Run(name, func(t *testing.T) {
			r := testRBAC(t, scope)
			if got := jwksRules(r.grantObjects()); len(got) != 0 {
				t.Fatalf("a grant with no issuer carries a JWKS rule: %v", got)
			}
			r.IssuerJWKS = true
			want := "ClusterRole forge-connect-prod-us-prod: /openid/v1/jwks get"
			got := jwksRules(r.grantObjects())
			if len(got) != 1 || got[0] != want {
				t.Fatalf("jwks rules = %v, want [%s]", got, want)
			}
			// Bound to the binding's own subject, like every other object of it.
			var binding *rbacObject
			for _, o := range r.grantObjects() {
				if o.Kind == "ClusterRoleBinding" {
					o := o
					binding = &o
				}
			}
			if binding == nil || !strings.Contains(binding.yaml(), "name: forge-connect-prod-us-prod\n  namespace: forge-system") {
				t.Fatalf("the ClusterRole is not bound to the binding's identity: %+v", binding)
			}
		})
	}
}

func TestConnect_TheGrantCarriesTheJWKSReadUnlessSkipped(t *testing.T) {
	for mode, want := range map[string]bool{"": true, issuerUpload: true, issuerSkip: false, issuerWithdraw: false} {
		s := vkeStub(t)
		stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "k1")))
		if out, err := connectWith(t, s, "vke-prod", mode, nil); err != nil {
			t.Fatalf("%q: %v\n%s", mode, err, out)
		}
		applied := strings.Join(s.applied, "\n")
		if has := strings.Contains(applied, "/openid/v1/jwks"); has != want {
			t.Errorf("--issuer %q: grant has the JWKS rule = %v, want %v\n%s", mode, has, want, applied)
		}
	}
}

func TestDryRun_ShowsTheIssuerAndTheJWKSRuleAndContactsNothing(t *testing.T) {
	s := vkeStub(t)
	stubIssuer(t, testIssuerURL, jwksJSON(t, testECJWK(t, "k1"), testECJWK(t, "k2")))
	out, err := connectWith(t, s, "vke-prod", "", func(o *clusterConnectOptions) { o.DryRun = true })
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if s.caller.connects != 0 || len(s.applied) != 0 {
		t.Fatalf("--dry-run wrote or contacted something")
	}
	for _, want := range []string{"issuer       " + testIssuerURL + " (upload; 2 public key(s) uploaded, set ", "url /openid/v1/jwks: get"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run omits %q:\n%s", want, out)
		}
	}
	if readConnectIssuerRecord("vke") != nil {
		t.Error("--dry-run kept an issuer record")
	}
}

func readIssuerRecord(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
