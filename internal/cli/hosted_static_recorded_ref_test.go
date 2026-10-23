package cli

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/hostedimage"
)

// WHAT FORGE PUSHED IS WHAT THE SPEC RECORDS — proved end to end, through the
// real commands, for a BARE hosted frontend.
//
// This is the invariant cp's StaticSite operator depends on (cp #501,
// internal/operators/staticsite/release.go): it pulls
// `spec.releaseRepository@spec.liveDigest` verbatim rather than recomposing a
// path from a registry base and an org id. The defect that rule exists to
// prevent was two independent derivations of one address — forge pushed
// `<image>/static.v1` while the operator recomposed
// `<base>/static.v1/<site>` — which 404'd on an artifact that existed.
//
// internal/deploytarget's hosted_static_release_ref_test.go pins the PUBLISH
// half from a stated group. What that cannot see is whether the ref the
// publish carries is the ref the BUILD actually pushed to, because it never
// runs a build. That join is what this test closes, and ADR-0003 F1 makes it
// worth re-proving: a bare image is now resolved by forge against the push
// base, so there are two more places the two halves could diverge.
func TestHostedStaticBareImageRecordsExactlyTheRefItPushed(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	// The base the env DECLARES, composed the one way forge composes it:
	// `<registry_host>/<organization>/<project>`. The control plane is
	// never asked, so there is no loop to seed and nothing on disk to go
	// stale — which is precisely what makes the two halves below agree.
	const pushBase = testStaticRegistryHost + "/" + testStaticOrg + "/acme"

	fake := newFakeDeployService(map[string]string{})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir := writeBareHostedStaticProject(t, srv.URL)
	t.Chdir(dir)
	t.Setenv("ACME_CP_TOKEN", "rlat_e2e")
	t.Setenv("FORGE_HOME", t.TempDir())
	prevPoll := hostedPollInterval
	hostedPollInterval = time.Millisecond
	t.Cleanup(func() { hostedPollInterval = prevPoll })
	stubHostedBundleRegistry(t, fake)

	// The env declares a platform registry_host, so the build authenticates
	// to it before pushing (ADR-0003 F3). Stubbed for the same reason the
	// push below is: the subject here is the ADDRESS the build recorded, and
	// a real `docker login` would make this test need a reachable registry
	// and write a credential into the developer's own keychain.
	stubDockerLoginArgs(t)

	// Capture the repository the build PUSHED to. This is the left-hand side
	// of the equality under test; nothing else in the test is allowed to
	// recompute it.
	var pushedTo string
	prevPush := hostedStaticPusher
	hostedStaticPusher = func(_ context.Context, _, repository string, _ deploytarget.StaticSiteFrontend) (string, error) {
		pushedTo = repository
		return hostedStaticDigest, nil
	}
	t.Cleanup(func() { hostedStaticPusher = prevPush })

	if out, err := runForge(t, "env", "build", "hosted", "--push", "--tag", "t1"); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	// The bare image resolved to the platform's base, and the layout segment
	// was appended to the RESULT — not to the bare name, which would have
	// produced `web/static.v1` with no registry at all.
	if want := pushBase + "/web/static.v1"; pushedTo != want {
		t.Fatalf("site pushed to %q, want %q", pushedTo, want)
	}
	if out, err := runForge(t, "env", "build", "hosted", "--release", "v1", "--no-build"); err != nil {
		t.Fatalf("record the release: %v\n%s", err, out)
	}
	if out, err := runForge(t, "env", "deploy", "hosted", "v1", "--yes", "--no-wait"); err != nil {
		t.Fatalf("promote: %v\n%s", err, out)
	}
	envID := fake.envs["hosted"]
	if out, err := runForge(t, "env", "deploy", "hosted", "--yes", "--no-wait", "--rollout-timeout", "2s"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}

	d := fake.bundles[envID]["web"]
	if d == nil {
		t.Fatal("no web record in the recorded bundle")
	}
	// THE EQUALITY. The published spec's repository is byte-identical to the
	// repository the build pushed to, and its digest is the one the push
	// returned. Either half alone is unpullable.
	if got := d.Spec["releaseRepository"]; got != pushedTo {
		t.Errorf("spec.releaseRepository = %v, but the build pushed to %q — the operator would pull an address nobody wrote to",
			got, pushedTo)
	}
	if got := d.Spec["liveDigest"]; got != hostedStaticDigest {
		t.Errorf("spec.liveDigest = %v, want the pushed digest %s", got, hostedStaticDigest)
	}
	// And the recorded ref is a real address: it names a registry host, so
	// the operator needs to recompose nothing.
	repo, _ := d.Spec["releaseRepository"].(string)
	if !hostedimage.HasRegistryHost(repo) {
		t.Errorf("spec.releaseRepository = %q names no registry host", repo)
	}
	// The release ledger keys the artifact by the same address, which is what
	// makes the deploy's lookup and the build's push one coordinate.
	rel := fake.releases["v1"]
	if len(rel.Artifacts) != 1 || rel.Artifacts[0].Name != pushedTo {
		t.Errorf("release artifact = %+v, want one keyed by %q", rel.Artifacts, pushedTo)
	}
}

// writeBareHostedStaticProject is writeHostedStaticProject with a BARE
// frontend image: the shape ADR-0003 F1 makes legal, and the one this test
// exists to follow all the way to the published spec.
const (
	testStaticRegistryHost = "registry.reliant.dev"
	testStaticOrg          = "4f3c2b1a-0000-4000-8000-000000000001"
)

func writeBareHostedStaticProject(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"forge.yaml":         "name: acme\nmodule_path: github.com/example/acme\n\n",
		"deploy/kcl/kcl.mod": "[package]\nname = \"acme_deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/hosted/main.k": `import forge

_bundle = forge.Bundle {
    project = "acme"
    env = "hosted"
    control_plane = forge.ControlPlane {
        endpoint = "` + endpoint + `"
        token_env = "ACME_CP_TOKEN"
        registry_host = "` + testStaticRegistryHost + `"
    }
    frontends = [forge.Frontend {
        name = "web"
        image = "web"
        path = "frontends/web"
        type = "vite"
        public_dir = "dist"
        base_path = "/app"
        runtime = forge.OnHosted {}
    }]
}

output = forge.render(_bundle)
`,
	}
	for rel, body := range files {
		writeStateFile(t, dir, rel, body)
	}
	markServiceProject(t, dir)
	return dir
}

// writeStateFile writes one file under dir, creating parents.
func writeStateFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A HOST-BEARING hosted frontend is unchanged by ADR-0003 F1: the declared
// reference plus the layout segment, verbatim. Kept beside the bare case so
// the two cannot drift apart silently — the resolution must add a registry
// where one is missing and change nothing where one is present.
func TestHostedStaticHostBearingImageIsUnchanged(t *testing.T) {
	ents := &KCLEntities{Frontends: []FrontendEntity{{
		Name: "web", Image: "ghcr.io/acme/web", Runtime: FrontendRuntime{Type: RuntimeHosted},
	}}}
	got := declaredImageDestinationsWithBase(ents, "registry.reliant.dev/org-7")
	if len(got) != 1 || got[0].repository != "ghcr.io/acme/web/static.v1" {
		t.Fatalf("destinations = %+v, want ghcr.io/acme/web/static.v1 verbatim", got)
	}
	if strings.Contains(got[0].repository, "registry.reliant.dev") {
		t.Error("the push base was composed onto a reference that already named a host")
	}
}
