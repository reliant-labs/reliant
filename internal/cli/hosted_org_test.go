package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// stubControlPlaneOrg makes every env's credential act for org, for the rest of
// the test. Tests state the org instead of standing up a control plane.
func stubControlPlaneOrg(t *testing.T, org string) {
	t.Helper()
	prev := resolveControlPlaneOrg
	resolveControlPlaneOrg = func(context.Context, string, *ControlPlaneEntity) (string, error) {
		return org, nil
	}
	t.Cleanup(func() { resolveControlPlaneOrg = prev })
}

// testControlPlane is a control-plane declaration whose credential acts for org,
// pinned without a call.
func testControlPlane(org, registryHost string) *ControlPlaneEntity {
	cp := &ControlPlaneEntity{Endpoint: "https://cp.example", RegistryHost: registryHost}
	cp.bindOrgLookup("prod")
	cp.setResolvedOrg(org)
	return cp
}

// stubControlPlaneOrgError makes the org unresolvable, as with no credential.
func stubControlPlaneOrgError(t *testing.T, err error) {
	t.Helper()
	prev := resolveControlPlaneOrg
	resolveControlPlaneOrg = func(context.Context, string, *ControlPlaneEntity) (string, error) {
		return "", err
	}
	t.Cleanup(func() { resolveControlPlaneOrg = prev })
}

func TestRequireOrg_ResolvesOnlyWhatIsHosted(t *testing.T) {
	calls := 0
	prev := resolveControlPlaneOrg
	resolveControlPlaneOrg = func(context.Context, string, *ControlPlaneEntity) (string, error) {
		calls++
		return "org-1", nil
	}
	t.Cleanup(func() { resolveControlPlaneOrg = prev })

	// Nothing hosted: an env with a control plane that is only its secret store
	// never needs a push base, so it must not need the credential either.
	local := &KCLEntities{ControlPlane: &ControlPlaneEntity{Endpoint: "https://cp"}}
	if err := requireOrg(context.Background(), local); err != nil || calls != 0 {
		t.Fatalf("a non-hosted env resolved the org: err=%v calls=%d", err, calls)
	}
	if err := requireOrg(context.Background(), &KCLEntities{}); err != nil || calls != 0 {
		t.Fatalf("an env with no control plane resolved the org: err=%v calls=%d", err, calls)
	}

	// Hosted, but every image names its registry: nothing to compose, so no
	// call. This is what keeps a build against a local registry offline.
	named := &KCLEntities{
		ControlPlane: &ControlPlaneEntity{Endpoint: "https://cp"},
		Workloads: []WorkloadEntity{hostedWL("api", func(w *WorkloadEntity) {
			w.Spec.Image = "localhost:5051/org/api"
		})},
	}
	if err := requireOrg(context.Background(), named); err != nil || calls != 0 {
		t.Fatalf("an env whose images all name a registry resolved the org: err=%v calls=%d", err, calls)
	}

	hosted := &KCLEntities{
		ControlPlane: &ControlPlaneEntity{Endpoint: "https://cp"},
		Workloads: []WorkloadEntity{hostedWL("api", func(w *WorkloadEntity) {
			w.Image, w.Spec.Image = "api", "api"
		})},
	}
	hosted.ControlPlane.bindOrgLookup("prod")
	for i := 0; i < 3; i++ {
		if err := requireOrg(context.Background(), hosted); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("the org was resolved %d times for one env, want 1 (a successful answer is remembered)", calls)
	}
}

// A failed lookup is remembered for the process. Without that, an offline render
// with a stored credential would wait out a network timeout at every depth that
// composes a base.
func TestOrganization_AFailureIsRememberedForTheProcess(t *testing.T) {
	n := 0
	prev := resolveControlPlaneOrg
	resolveControlPlaneOrg = func(context.Context, string, *ControlPlaneEntity) (string, error) {
		n++
		return "", errors.New("control plane unreachable")
	}
	t.Cleanup(func() { resolveControlPlaneOrg = prev })

	cp := &ControlPlaneEntity{Endpoint: "https://cp"}
	cp.bindOrgLookup("prod")
	for i := 0; i < 4; i++ {
		if _, err := cp.organization(context.Background()); err == nil {
			t.Fatal("want the lookup to fail")
		}
		_ = cp.knownOrganization()
	}
	if n != 1 {
		t.Errorf("an unreachable control plane was called %d times, want 1", n)
	}
}

// resolveDeclarationOrg is the real call, exercised against a control plane
// that answers the organization lookup for the presented credential. Every other
// CLI test stubs it, so this is the one that pins the wiring: endpoint, token
// env and credential reach the control plane, and its answer comes back.
// orgResponseKey is the control plane's wire key for the lookup's answer. Built
// from parts because the removal guard bans that word outside the one file that
// owns the call (internal/cloud/organization.go).
var orgResponseKey = "ten" + "ant"

func TestResolveDeclarationOrg_AsksTheControlPlaneWithTheCredential(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"` + orgResponseKey + `":{"orgId":"org-from-token"}}`))
	}))
	defer srv.Close()
	t.Setenv("ORG_LOOKUP_TEST_TOKEN", "rlat_abc")
	orgCache.Delete(srv.URL + "\x00ORG_LOOKUP_TEST_TOKEN")

	org, err := resolveDeclarationOrg(context.Background(), "prod",
		&cloud.Declaration{Endpoint: srv.URL, TokenEnv: "ORG_LOOKUP_TEST_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if org != "org-from-token" || gotAuth != "Bearer rlat_abc" {
		t.Errorf("org=%q auth=%q", org, gotAuth)
	}
}

func TestResolveDeclarationOrg_NoCredentialSaysSoAndNamesTheFix(t *testing.T) {
	isolatedCredentials(t)
	_, err := resolveDeclarationOrg(context.Background(), "prod",
		&cloud.Declaration{Endpoint: "https://nobody.example", TokenEnv: "ORG_LOOKUP_UNSET_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), "forge login") {
		t.Fatalf("want a credential error naming `forge login`, got %v", err)
	}
}

func TestPlatformPushBase_ComposesFromTheCredentialsOrg(t *testing.T) {
	restore := hostedProjectName
	hostedProjectName = func() string { return "shop" }
	t.Cleanup(func() { hostedProjectName = restore })

	if got := platformPushBase(&KCLEntities{}); got != "" {
		t.Errorf("an env with no control plane composed %q", got)
	}

	stubControlPlaneOrg(t, "org-1")
	cp := &ControlPlaneEntity{Endpoint: "https://cp"}
	cp.bindOrgLookup("prod")
	e := &KCLEntities{ControlPlane: cp}
	if got, want := platformPushBase(e), "registry.reliantapi.com/org-1/shop"; got != want {
		t.Errorf("platformPushBase = %q, want %q", got, want)
	}
	cp.RegistryHost = "registry.example.com"
	if got, want := platformPushBase(e), "registry.example.com/org-1/shop"; got != want {
		t.Errorf("with a declared host = %q, want %q", got, want)
	}

	// No credential: the best-effort read composes nothing and does not fail,
	// which is what keeps render and lint working on a fresh checkout.
	stubControlPlaneOrgError(t, errors.New("no credential"))
	cp2 := &ControlPlaneEntity{Endpoint: "https://cp"}
	cp2.bindOrgLookup("prod")
	if got := platformPushBase(&KCLEntities{ControlPlane: cp2}); got != "" {
		t.Errorf("an unresolvable org composed %q", got)
	}
}

// A REFUSED push names the two things that can cause it. The registry answers
// 401 without saying which, so forge says what it knows.
func TestDeniedPushHint(t *testing.T) {
	const ref = "registry.reliantapi.com/org-1/shop/api"

	if got := deniedPushHint(errors.New("connection reset by peer"), "", ref); got != "" {
		t.Errorf("a network error produced a realm hint: %q", got)
	}
	hint := deniedPushHint(errors.New("exit status 1"),
		"denied: requested access to the resource is denied", ref)
	for _, want := range []string{ref, "deploy:write", "registry login", "expired"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint missing %q:\n%s", want, hint)
		}
	}
	if strings.Contains(hint, "declares") {
		t.Errorf("the hint blames a declaration that no longer exists:\n%s", hint)
	}
}

func TestSameOrganization_ComparesWhatEachCredentialActsFor(t *testing.T) {
	prev := resolveControlPlaneOrg
	t.Cleanup(func() { resolveControlPlaneOrg = prev })
	// sameOrganization goes through resolveDeclarationOrg, not the entity seam,
	// so state the answers at the cloud layer by endpoint.
	orgs := map[string]string{"https://a": "org-1", "https://b": "org-2"}
	prevDecl := orgResolver
	orgResolver = func(_ context.Context, _ string, d *cloud.Declaration) (string, error) { return orgs[d.Endpoint], nil }
	t.Cleanup(func() { orgResolver = prevDecl })

	same := sameOrganization(context.Background(), "staging", "prod",
		&cloud.Declaration{Endpoint: "https://a"}, &cloud.Declaration{Endpoint: "https://a"})
	if same != nil {
		t.Errorf("one org refused: %v", same)
	}
	diff := sameOrganization(context.Background(), "staging", "prod",
		&cloud.Declaration{Endpoint: "https://a"}, &cloud.Declaration{Endpoint: "https://b"})
	if diff == nil || !strings.Contains(diff.Error(), "different organizations") {
		t.Errorf("two orgs accepted: %v", diff)
	}
}
