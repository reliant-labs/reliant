package cli

// The bundle push authenticates to the platform registry with the env's
// control-plane identity — in memory, through the registry's own token realm —
// and never through the docker credential store.
//
// THE FAKE IS A REAL TOKEN-REALM REGISTRY (httptest, no network): the registry
// answers every request without a minted bearer token with a 401 challenge, the
// realm mints a token only for the credential it is configured to accept, and
// the registry accepts only tokens the realm minted. That is the shape that
// produced the production 401 — a realm that rejects a stale credential — so a
// test against it fails for the reason the bug did, not for a reason invented
// to fit the fix.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	bundleTestToken      = "rlat_fresh_control_plane_credential"
	bundleTestStaleToken = "rlat_stale_written_long_ago"
	bundleTestTokenEnv   = "FORGE_TEST_BUNDLE_REGISTRY_TOKEN"
)

type realmHit struct{ user, password string }

type fakeRealmRegistry struct {
	srv *httptest.Server

	mu         sync.Mutex
	accepts    string
	realmHits  []realmHit
	minted     map[string]bool
	rejected   int
	pushedBlob map[string][]byte
	bearerReqs int
}

func newFakeRealmRegistry(t *testing.T, accepts string) *fakeRealmRegistry {
	t.Helper()
	f := &fakeRealmRegistry{accepts: accepts, minted: map[string]bool{}, pushedBlob: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	// A loopback registry takes no credentials by forge's own rule; this one
	// stands in for registry.reliantapi.com, which does.
	prev := registryTakesNoCredentials
	registryTakesNoCredentials = func(string) bool { return false }
	t.Cleanup(func() { registryTakesNoCredentials = prev })
	return f
}

func (f *fakeRealmRegistry) host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func (f *fakeRealmRegistry) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/auth/token" {
		_, password, _ := r.BasicAuth()
		user, _, _ := r.BasicAuth()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.realmHits = append(f.realmHits, realmHit{user: user, password: password})
		if password != f.accepts {
			f.rejected++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"errors":[{"code":"UNAUTHORIZED","message":"the supplied credential is not valid"}]}`)
			return
		}
		tok := fmt.Sprintf("jwt-%d", len(f.minted)+1)
		f.minted[tok] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_in": 300})
		return
	}

	f.mu.Lock()
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	ok := f.minted[bearer]
	if ok {
		f.bearerReqs++
	}
	f.mu.Unlock()
	if !ok {
		w.Header().Set("WWW-Authenticate",
			fmt.Sprintf(`Bearer realm="%s/auth/token",service="fake-registry",scope="repository:x:push,pull"`, f.srv.URL))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
		w.Header().Set("Location", strings.TrimSuffix(r.URL.Path, "/")+"/session")
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.pushedBlob[r.URL.Query().Get("digest")] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		d := "sha256:" + hex.EncodeToString(sum[:])
		f.mu.Lock()
		f.pushedBlob[d] = body
		f.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", d)
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodHead:
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeRealmRegistry) passwordsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.realmHits))
	for _, h := range f.realmHits {
		out = append(out, h.password)
	}
	return out
}

// useControlPlaneIdentity states the env's control-plane declaration and the
// credential forge resolves for it: the real resolution path (declared
// token_env), pointed at host as the platform registry. The cache key is the
// test's own project dir, so no test sees another's identity.
func useControlPlaneIdentity(t *testing.T, env, registryHost, token string) {
	t.Helper()
	t.Setenv(bundleTestTokenEnv, token)
	prev := bundleRegistryAuthFor
	bundleRegistryAuthFor = func(_ context.Context, _, _ string) (bundleRegistryAuth, error) {
		return bundleRegistryAuthOf(env, &KCLEntities{ControlPlane: &ControlPlaneEntity{
			Type: "control_plane", Endpoint: "https://control-plane.test",
			TokenEnv: bundleTestTokenEnv, RegistryHost: registryHost,
		}})
	}
	t.Cleanup(func() { bundleRegistryAuthFor = prev })
}

// useDockerStore points the docker credential store at a fresh config dir
// holding entry for host, and returns the config.json path.
func useDockerStore(t *testing.T, host, entryUser, entryPassword string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	cfg := filepath.Join(dir, "config.json")
	body := "{}"
	if host != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(entryUser + ":" + entryPassword))
		body = fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, host, auth)
	}
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func builtTestBundle(t *testing.T) bundle.Bundle {
	t.Helper()
	b, err := bundle.Build(context.Background(), bundle.BuildInput{
		Project: "acme", Env: "prod", Release: "v1.0.0",
		Shape:     bundle.ShapeInput{Kind: release.EnvSelfManaged, Manifests: bundleTestStream},
		CreatedAt: bundleTestNow,
	})
	if err != nil {
		t.Fatalf("build the test bundle: %v", err)
	}
	return b
}

func noDockerLogin(t *testing.T) {
	t.Helper()
	prev := dockerLogin
	dockerLogin = func(context.Context, []string, io.Reader) error {
		t.Error("`docker login` ran: the bundle push must authenticate itself, not borrow a login")
		return nil
	}
	t.Cleanup(func() { dockerLogin = prev })
}

// The push logs in through the control-plane credential path: the realm sees
// the env's declared credential (never anything else), and the registry
// accepts the token it minted.
func TestBundlePush_AuthenticatesThroughTheRealmWithTheControlPlaneCredential(t *testing.T) {
	reg := newFakeRealmRegistry(t, bundleTestToken)
	useControlPlaneIdentity(t, "prod", reg.host(), bundleTestToken)
	useDockerStore(t, "", "", "")
	noDockerLogin(t)

	built := builtTestBundle(t)
	digest, err := pushBundleTo(context.Background(), t.TempDir(), "prod", reg.host()+"/org/proj/bundle.v1/prod", built)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if digest != built.Digest {
		t.Errorf("pushed %s, want %s", digest, built.Digest)
	}
	if got := reg.passwordsSeen(); len(got) == 0 || got[0] != bundleTestToken {
		t.Errorf("the realm must be shown the control-plane credential, saw %v", got)
	}
	for _, p := range reg.passwordsSeen() {
		if p != bundleTestToken {
			t.Errorf("the realm saw a credential that is not the control plane's: %q", p)
		}
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.bearerReqs == 0 {
		t.Error("the blob and manifest requests must carry the realm's short-lived token")
	}
	if _, ok := reg.pushedBlob[built.Digest]; !ok {
		t.Error("the manifest never reached the registry")
	}
}

// A stale entry in the docker credential store must not shadow the fresh
// credential — and the control: the SAME registry refuses a push that goes
// through the store, which is the production 401. If this control ever passes,
// the test above no longer proves anything.
func TestBundlePush_AStaleDockerStoreEntryDoesNotShadowTheFreshCredential(t *testing.T) {
	reg := newFakeRealmRegistry(t, bundleTestToken)
	useControlPlaneIdentity(t, "prod", reg.host(), bundleTestToken)
	cfg := useDockerStore(t, reg.host(), "forge", bundleTestStaleToken)
	before, _ := os.ReadFile(cfg)
	built := builtTestBundle(t)
	repo := reg.host() + "/org/proj/bundle.v1/prod"

	// Control: the store path presents the stale entry and is refused.
	stale, err := bundle.NewRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.Push(context.Background(), stale, built); err == nil {
		t.Fatal("control: a push through the docker store with a stale entry must be refused (the production 401)")
	}
	if !sliceHas(reg.passwordsSeen(), bundleTestStaleToken) {
		t.Fatalf("control: the store entry was never presented (%v); the test is not exercising the store", reg.passwordsSeen())
	}

	// The fix: the same registry, the same stale store, a successful push.
	reg.mu.Lock()
	reg.realmHits = nil
	reg.mu.Unlock()
	if _, err := pushBundleTo(context.Background(), t.TempDir(), "prod", repo, built); err != nil {
		t.Fatalf("a stale store entry shadowed the fresh credential: %v", err)
	}
	if sliceHas(reg.passwordsSeen(), bundleTestStaleToken) {
		t.Errorf("the stale store entry was presented to the realm: %v", reg.passwordsSeen())
	}
	if after, _ := os.ReadFile(cfg); string(after) != string(before) {
		t.Errorf("the push wrote to the docker credential store:\n before %s\n after  %s", before, after)
	}
}

// No image was pushed to the platform host, so nothing ever logged in to it
// (control-plane prod pushes images to GAR). The bundle push authenticates
// anyway.
func TestBundlePush_AuthenticatesWhenNoImageWasPushedToTheHost(t *testing.T) {
	reg := newFakeRealmRegistry(t, bundleTestToken)
	useControlPlaneIdentity(t, "prod", reg.host(), bundleTestToken)
	useDockerStore(t, "", "", "")
	noDockerLogin(t)
	if _, done := platformLogins.Load(reg.host()); done {
		t.Fatal("precondition: no login to the platform host has run in this process")
	}

	// The images' destination is GAR: the image-push login has no reason to run.
	plan := pushPlan{push: true, destinations: []imageDestination{
		{repository: "us-central1-docker.pkg.dev/acme/reliant-prod/api", workload: "api"},
	}}
	if err := autoLoginForPush(context.Background(), "prod",
		&KCLEntities{ControlPlane: &ControlPlaneEntity{Endpoint: "https://control-plane.test", RegistryHost: reg.host()}}, plan); err != nil {
		t.Fatalf("autoLoginForPush: %v", err)
	}
	if _, done := platformLogins.Load(reg.host()); done {
		t.Fatal("precondition: an image-less push logs in to nothing")
	}

	if _, err := pushBundleTo(context.Background(), t.TempDir(), "prod", reg.host()+"/org/proj/bundle.v1/prod", builtTestBundle(t)); err != nil {
		t.Fatalf("the bundle push must authenticate on its own: %v", err)
	}
}

// A registry that is not the control plane's gets the docker store, not the
// control-plane credential: the platform token is never sent to a host it was
// not issued for.
func TestBundlePush_NeverPresentsTheControlPlaneCredentialToAnotherRegistry(t *testing.T) {
	platform := newFakeRealmRegistry(t, bundleTestToken)
	other := newFakeRealmRegistry(t, "their-own-password")
	useControlPlaneIdentity(t, "prod", platform.host(), bundleTestToken)
	useDockerStore(t, other.host(), "me", "their-own-password")

	if _, err := pushBundleTo(context.Background(), t.TempDir(), "prod", other.host()+"/theirs/bundle.v1/prod", builtTestBundle(t)); err != nil {
		t.Fatalf("a registry the author owns is reached with the author's docker login: %v", err)
	}
	if sliceHas(other.passwordsSeen(), bundleTestToken) {
		t.Errorf("the control-plane credential was sent to a foreign registry: %v", other.passwordsSeen())
	}
	if len(platform.passwordsSeen()) != 0 {
		t.Errorf("the platform realm must not be contacted for another host: %v", platform.passwordsSeen())
	}
}

// A 401 fails clearly: it names the registry, the identity presented and the
// fix, and it says the docker store was not the cause.
func TestBundlePush_ARefusedCredentialNamesTheRegistryTheIdentityAndTheFix(t *testing.T) {
	reg := newFakeRealmRegistry(t, "a-credential-forge-does-not-hold")
	useControlPlaneIdentity(t, "prod", reg.host(), bundleTestToken)
	useDockerStore(t, "", "", "")

	_, err := pushBundleTo(context.Background(), t.TempDir(), "prod", reg.host()+"/org/proj/bundle.v1/prod", builtTestBundle(t))
	var denied *bundleRegistryDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("a refused credential must be a *bundleRegistryDeniedError, got %T: %v", err, err)
	}
	msg := err.Error()
	for name, want := range map[string]string{
		"the registry":     reg.host(),
		"the repository":   "/org/proj/bundle.v1/prod",
		"the status":       "401",
		"the identity":     "control-plane credential for https://control-plane.test",
		"its source":       "env: " + bundleTestTokenEnv,
		"the fix":          "fix:",
		"the CI remedy":    "$" + bundleTestTokenEnv,
		"deploy:write":     "deploy:write",
		"not the store":    "did not read the docker credential store",
		"the realm's word": "the supplied credential is not valid",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error must name %s (%q); got:\n%s", name, want, msg)
		}
	}
	if strings.Contains(msg, bundleTestToken) {
		t.Errorf("the error leaked the credential value:\n%s", msg)
	}
}

// With no credential at all the error is raised before any request and names
// the env, the registry and the remedies.
func TestBundlePush_NoCredentialFailsBeforeTheRegistryIsContacted(t *testing.T) {
	reg := newFakeRealmRegistry(t, bundleTestToken)
	t.Setenv(bundleTestTokenEnv, "")
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	t.Setenv("FORGE_HOME", t.TempDir())
	prev := bundleRegistryAuthFor
	bundleRegistryAuthFor = func(_ context.Context, _, _ string) (bundleRegistryAuth, error) {
		return bundleRegistryAuthOf("prod", &KCLEntities{ControlPlane: &ControlPlaneEntity{
			Type: "control_plane", Endpoint: "https://control-plane.test",
			TokenEnv: bundleTestTokenEnv, RegistryHost: reg.host(),
		}})
	}
	t.Cleanup(func() { bundleRegistryAuthFor = prev })

	_, err := pushBundleTo(context.Background(), t.TempDir(), "prod", reg.host()+"/org/proj/bundle.v1/prod", builtTestBundle(t))
	if err == nil {
		t.Fatal("a push with no credential must fail")
	}
	for _, want := range []string{`env "prod"`, reg.host(), bundleTestTokenEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must contain %q; got:\n%s", want, err)
		}
	}
	if len(reg.passwordsSeen()) != 0 {
		t.Errorf("no request should have reached the realm: %v", reg.passwordsSeen())
	}
}

// ── policy: what a failed push does to the build ────────────────────────────

func stubHostedBundleLedger(t *testing.T, hub bool, base string) {
	t.Helper()
	prev := bundleLedgerFor
	bundleLedgerFor = func(context.Context, string, string) (envLedger, error) {
		return envLedger{Bindings: hostedBindingsStub{}, Releases: hostedReleasesStub{}, Hosted: true, Units: hubUnitsIf(hub)}, nil
	}
	t.Cleanup(func() { bundleLedgerFor = prev })
	prevRec := bundleRecorderForEnv
	bundleRecorderForEnv = func(_ context.Context, projectDir, _ string, _ envLedger) (bundleRecorder, error) {
		return recordStoreFor(projectDir)
	}
	t.Cleanup(func() { bundleRecorderForEnv = prevRec })
}

// A registry that ANSWERED and said no fails the build in every hosted env: the
// bundle did not reach the registry for a reason a retry will not change.
func TestEnvBuildBundle_ARefusedPushFailsTheBuildEvenForALedgerOnlyEnv(t *testing.T) {
	reg := newFakeRealmRegistry(t, "not-the-credential")
	dir := newLedgerTestProject(t, "bundle-denied-project")
	stubEnvShape(t, "bundle-denied-project")
	writeHostedPushBaseFixture(t, dir, "prod", reg.host()+"/org/proj")
	useControlPlaneIdentity(t, "prod", reg.host(), bundleTestToken)
	useDockerStore(t, "", "", "")
	stubHostedBundleLedger(t, false, "")

	_, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
		Release: "v1.4.0", Now: bundleTestNow, Pushed: true, errOut: io.Discard,
	})
	var denied *bundleRegistryDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("a refused bundle push must fail the build, got: %v", err)
	}
	if n := len(mustBundles(t, dir, "prod")); n != 0 {
		t.Errorf("a refused push must record nothing, found %d rows", n)
	}
}

// An UNREACHABLE registry is tolerated for a ledger-only env (the next build
// pushes it) but not for a hub-converged one, whose control plane deploys only
// what it has recorded.
func TestEnvBuildBundle_AnUnreachableRegistryFailsAHubConvergedEnvOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hub     bool
		wantErr bool
	}{
		{"ledger-only env keeps the warning", false, false},
		{"hub-converged env refuses", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newLedgerTestProject(t, "bundle-unreachable-project")
			stubEnvShape(t, "bundle-unreachable-project")
			writeHostedPushBaseFixture(t, dir, "prod", "registry.unreachable.invalid/acme/proj")
			stubHostedBundleLedger(t, tc.hub, "")
			prev := bundlePushTarget
			bundlePushTarget = func(context.Context, string, string, string) (bundle.Pusher, error) {
				return nil, errors.New("dial tcp: lookup registry.unreachable.invalid: no such host")
			}
			t.Cleanup(func() { bundlePushTarget = prev })

			var warnings strings.Builder
			_, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
				Release: "v1.4.0", Now: bundleTestNow, Pushed: true, errOut: &warnings,
			})
			if tc.wantErr {
				if err == nil {
					t.Fatal("a hub-converged env whose bundle was not pushed is not deployable; the build must say so")
				}
				for _, want := range []string{"not deployable", "no such host", "forge env build prod"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("the error must contain %q; got:\n%s", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("a ledger-only env tolerates an unreachable registry: %v", err)
			}
			if !strings.Contains(warnings.String(), "could not be pushed") {
				t.Errorf("the degradation must still be reported; got %q", warnings.String())
			}
		})
	}
}

// End to end through the default seam: realm, push, and the record, with the
// docker store holding a stale entry the whole time.
func TestEnvBuildBundle_HostedPushThroughTheRealmIsRecorded(t *testing.T) {
	reg := newFakeRealmRegistry(t, bundleTestToken)
	dir := newLedgerTestProject(t, "bundle-realm-project")
	stubEnvShape(t, "bundle-realm-project")
	writeHostedPushBaseFixture(t, dir, "prod", reg.host()+"/org/proj")
	useControlPlaneIdentity(t, "prod", reg.host(), bundleTestToken)
	useDockerStore(t, reg.host(), "forge", bundleTestStaleToken)
	noDockerLogin(t)
	stubHostedBundleLedger(t, true, "")

	written, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
		Release: "v1.4.0", Now: bundleTestNow, Pushed: true, errOut: io.Discard,
	})
	if err != nil {
		t.Fatalf("writeEnvBundles: %v", err)
	}
	got := written[0]
	if !got.Pushed || !got.Recorded {
		t.Fatalf("the bundle must be pushed and recorded: %+v", got)
	}
	if want := reg.host() + "/org/proj/bundle.v1/prod@" + got.Digest; got.Reference != want {
		t.Errorf("reference = %q, want %q", got.Reference, want)
	}
	if sliceHas(reg.passwordsSeen(), bundleTestStaleToken) {
		t.Errorf("the stale store entry reached the realm: %v", reg.passwordsSeen())
	}
}

func sliceHas(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func hubUnitsIf(hub bool) unitSplit {
	if hub {
		return allHubUnits()
	}
	return unitSplit{}
}
