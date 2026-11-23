package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// pgoShell is a ShellBuild over cwd whose pgo names the given binaries.
func pgoShell(cwd string, pgo map[string]ShellPGOConfig, command ...string) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		w.Build = BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "./scripts/build-image.sh", Cwd: cwd, PGO: pgo}}
		w.Spec.Command = command
	}
}

// One sibling-repo image runs three roles (control-plane's reliant
// ShellBuild over ../reliant): ONE binary, keyed by its project-relative
// package, whose profile lives in the sibling checkout and merges all three.
func TestResolvePGOBinaries_ShellBuildSibling(t *testing.T) {
	reliant := pgoShell("../reliant", map[string]ShellPGOConfig{"./cmd/reliant": {}})
	got := pgoByPackage(t, &KCLEntities{Project: "control-plane", Env: "prod", Workloads: []WorkloadEntity{
		clusterWL("reliant-api-server", "k3d-x", "ns", reliant),
		clusterWL("reliant-temporal-worker", "k3d-x", "ns", reliant),
		clusterWL("daemon-gateway", "k3d-x", "ns", reliant),
		// A ShellBuild that names no binary contributes none: forge cannot
		// see what its command builds.
		clusterWL("opaque", "k3d-x", "ns", pgoShell("../other", nil)),
	}})
	if len(got) != 1 {
		t.Fatalf("want only the reliant binary, got %+v", got)
	}
	b := got["../reliant/cmd/reliant"]
	if b.ProfilePath() != "../reliant/cmd/reliant/default.pgo" || b.PGO.Out != b.ProfilePath() || !b.PGO.Enabled {
		t.Errorf("binary = %+v (profile %s)", b, b.ProfilePath())
	}
	if want := []string{"daemon-gateway", "reliant-api-server", "reliant-temporal-worker"}; !slices.Equal(b.Workloads, want) || !slices.Equal(b.Services, want) {
		t.Errorf("workloads %v services %v, want %v", b.Workloads, b.Services, want)
	}
}

func TestResolvePGOBinaries_ShellBuildShapes(t *testing.T) {
	two := map[string]ShellPGOConfig{"./cmd/api": {}, "./cmd/edge": {PGOConfig: PGOConfig{Since: "72h"}}}
	got := pgoByPackage(t, &KCLEntities{Project: "shop", Env: "prod", Workloads: []WorkloadEntity{
		// In the project itself, a ShellBuild binary is the GoBuild's: the
		// same package and profile, so the two agree on the defaults.
		clusterWL("api", "k3d-x", "ns", pgoGo("./cmd/api", nil)),
		clusterWL("api-image", "k3d-x", "ns", pgoShell("", two, "/usr/local/bin/api")),
		// Several binaries: the command's executable picks the one it runs.
		clusterWL("edge", "k3d-x", "ns", pgoShell("", two, "/app/edge", "--listen=:80")),
		clusterWL("job", "k3d-x", "ns", pgoShell("", two, "/bin/sh", "-c", "true")),
		// A source build is keyed by the repository's path and writes
		// where `out` says.
		clusterWL("pinned", "k3d-x", "ns", func(w *WorkloadEntity) {
			w.Build = BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "make", Source: &config.GitSource{Repo: "https://github.com/acme/billing.git", Ref: "v1"},
				PGO: map[string]ShellPGOConfig{"./cmd/billing": {Out: "profiles/billing.pgo", Namespace: "billing"}}}}
		}),
	}})
	if b := got["./cmd/api"]; b.PGO.Out != "" || b.ProfilePath() != "cmd/api/default.pgo" || !slices.Equal(b.Workloads, []string{"api", "api-image"}) {
		t.Errorf("./cmd/api = %+v", b)
	}
	if b := got["./cmd/edge"]; !slices.Equal(b.Workloads, []string{"edge"}) || b.PGO.Since.Hours() != 72 {
		t.Errorf("./cmd/edge = %+v", b)
	}
	b := got["github.com/acme/billing/cmd/billing"]
	if b.ProfilePath() != "profiles/billing.pgo" || b.PGO.Namespace != "billing" || !slices.Equal(b.Workloads, []string{"pinned"}) {
		t.Errorf("source binary = %+v (all: %v)", b, got)
	}
}

func TestResolvePGOBinaries_ShellBuildRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		e    *KCLEntities
		want string
	}{
		"two workloads read other namespaces": {e: &KCLEntities{Workloads: []WorkloadEntity{
			clusterWL("api", "k3d-x", "ns", pgoShell("../reliant", map[string]ShellPGOConfig{"./cmd/reliant": {Namespace: "reliant"}})),
			clusterWL("worker", "k3d-x", "ns", pgoShell("../reliant", map[string]ShellPGOConfig{"./cmd/reliant": {}})),
		}}, want: "one binary has one profile"},
		"two workloads write other files": {e: &KCLEntities{Workloads: []WorkloadEntity{
			clusterWL("api", "k3d-x", "ns", pgoShell("../reliant", map[string]ShellPGOConfig{"./cmd/reliant": {Out: "../reliant/cmd/reliant/x.pgo"}})),
			clusterWL("worker", "k3d-x", "ns", pgoShell("../reliant", map[string]ShellPGOConfig{"./cmd/reliant": {}})),
		}}, want: "one binary has one profile"},
		"source without out": {e: &KCLEntities{Workloads: []WorkloadEntity{
			clusterWL("api", "k3d-x", "ns", func(w *WorkloadEntity) {
				w.Build = BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "make", Source: &config.GitSource{Repo: "github.com/acme/api", Ref: "v1"},
					PGO: map[string]ShellPGOConfig{"./cmd/api": {}}}}
			}),
		}}, want: "ShellPGO.out must say where the profile goes"},
		"absolute out": {e: &KCLEntities{Workloads: []WorkloadEntity{
			clusterWL("api", "k3d-x", "ns", pgoShell("../reliant", map[string]ShellPGOConfig{"./cmd/reliant": {Out: "/src/default.pgo"}})),
		}}, want: "relative to the project root"},
		"bad window": {e: &KCLEntities{Workloads: []WorkloadEntity{
			clusterWL("api", "k3d-x", "ns", pgoShell("../reliant", map[string]ShellPGOConfig{"./cmd/reliant": {PGOConfig: PGOConfig{Since: "7d"}}})),
		}}, want: "PGO.since"},
	} {
		if _, err := ResolvePGOBinaries(tc.e); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `"api"`) {
			t.Errorf("%s: err = %v, want one naming workload api and %q", name, err, tc.want)
		}
	}
}

// The KCL render of a ShellBuild's pgo decodes into the same binaries.
func TestPGOShellDecodesFromTheRender(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "kcl", "tests", "positive_build_pgo_shell.k"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := parseKCLEntities(renderKCLProject(t, writeKCLProject(t, string(src))))
	if err != nil {
		t.Fatal(err)
	}
	got := pgoByPackage(t, e)
	r := got["../reliant/cmd/reliant"]
	if r.ProfilePath() != "../reliant/cmd/reliant/default.pgo" || r.PGO.FromEnv != "prod" || !slices.Equal(r.Services, []string{"reliant-api-server"}) {
		t.Errorf("reliant = %+v", r)
	}
	if a := got["./cmd/a"]; a.ProfilePath() != "../profiles/a.pgo" || a.PGO.Namespace != "other" || a.PGO.MinWeight.Milliseconds() != 500 {
		t.Errorf("./cmd/a = %+v", a)
	}
	if b := got["./cmd/b"]; b.PGO.Enabled {
		t.Errorf("./cmd/b = %+v, want disabled", b)
	}
	if p := got["github.com/acme/api/cmd/api"]; p.ProfilePath() != "profiles/api/default.pgo" {
		t.Errorf("source binary = %+v", p)
	}
}

func TestGitSourcePath(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/acme/api":             "github.com/acme/api",
		"https://github.com/acme/api.git": "github.com/acme/api",
		"ssh://git@github.com/acme/api":   "github.com/acme/api",
		"git@github.com:acme/api.git":     "github.com/acme/api",
	} {
		if got := gitSourcePath(in); got != want {
			t.Errorf("gitSourcePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// newPGOShellFixture is control-plane's layout: the project in <root>/cp and
// the reliant checkout beside it, whose one binary three workloads run.
func newPGOShellFixture(t *testing.T, cfg ShellPGOConfig) *pgoFixture {
	t.Helper()
	f := newPGOFixture(t)
	root := t.TempDir()
	f.dir = filepath.Join(root, "cp")
	for _, d := range []string{f.dir, filepath.Join(root, "reliant", "cmd", "reliant")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	reliant := pgoShell("../reliant", map[string]ShellPGOConfig{"./cmd/reliant": cfg})
	f.envs["prod"] = &KCLEntities{Project: "control-plane", Env: "prod", Workloads: []WorkloadEntity{
		clusterWL("reliant-api-server", "k3d-x", "ns", reliant),
		clusterWL("reliant-temporal-worker", "k3d-x", "ns", reliant),
		clusterWL("daemon-gateway", "k3d-x", "ns", reliant),
	}}
	return f
}

func TestPGOPull_ShellBuildWritesTheSiblingProfile(t *testing.T) {
	f := newPGOShellFixture(t, ShellPGOConfig{})
	doc, err := f.pull(t, pgoPullOpts{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	r := pgoOnly(t, doc, "../reliant/cmd/reliant")
	if r.Action != pgoWritten || r.Path != "../reliant/cmd/reliant/default.pgo" || r.Namespace != "control-plane" {
		t.Fatalf("result = %+v", r)
	}
	if p := f.local.params[0]; p["services"] != "['daemon-gateway','reliant-api-server','reliant-temporal-worker']" || p["namespace"] != "control-plane" {
		t.Errorf("query params = %v", p)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.dir), "reliant", "cmd", "reliant", "default.pgo")); err != nil {
		t.Errorf("the sibling's profile was not written: %v", err)
	}
	// --binary names it by the same package.
	if doc, err := f.pull(t, pgoPullOpts{binaries: []string{"../reliant/cmd/reliant/"}, dryRun: true}); err != nil || len(doc.Binaries) != 1 {
		t.Errorf("--binary ../reliant/cmd/reliant = %+v, %v", doc.Binaries, err)
	}
	// The report says to commit it in its own checkout.
	f.stdout.Reset()
	if err := runPGOPull(context.Background(), f.deps(), pgoPullOpts{}); err != nil {
		t.Fatal(err)
	}
	if out := f.stdout.String(); !strings.Contains(out, "git -C ../reliant/cmd/reliant add default.pgo") || strings.Contains(out, "git add ../") {
		t.Errorf("report:\n%s", out)
	}
}

func TestPGOPull_ShellBuildNamespaceAndOut(t *testing.T) {
	f := newPGOShellFixture(t, ShellPGOConfig{Namespace: "reliant", Out: "../reliant/cmd/reliant/prod.pgo"})
	doc, err := f.pull(t, pgoPullOpts{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if r := pgoOnly(t, doc, "../reliant/cmd/reliant"); r.Action != pgoWritten || r.Path != "../reliant/cmd/reliant/prod.pgo" || r.Namespace != "reliant" {
		t.Fatalf("result = %+v", r)
	}
	if p := f.local.params[0]; p["namespace"] != "reliant" {
		t.Errorf("query params = %v", p)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.dir), "reliant", "cmd", "reliant", "prod.pgo")); err != nil {
		t.Errorf("out was not written: %v", err)
	}
}

// Where a ShellBuild's profile cannot go, pull says why and writes nothing.
func TestPGOPull_ShellBuildSkips(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg    ShellPGOConfig
		setup  func(f *pgoFixture)
		reason string
	}{
		"sibling not cloned": {setup: func(f *pgoFixture) {
			if err := os.RemoveAll(filepath.Join(filepath.Dir(f.dir), "reliant")); err != nil {
				t.Fatal(err)
			}
		}, reason: "../reliant/cmd/reliant has no directory on disk"},
		"outside the workspace": {cfg: ShellPGOConfig{Out: "../../elsewhere/default.pgo"}, reason: "is outside the directory holding this project"},
		"disabled":              {cfg: ShellPGOConfig{PGOConfig: *pgoOff()}, reason: "PGO is off for ../reliant/cmd/reliant"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPGOShellFixture(t, tc.cfg)
			if tc.setup != nil {
				tc.setup(f)
			}
			doc, err := f.pull(t, pgoPullOpts{})
			if err != nil {
				t.Fatalf("pull: %v", err)
			}
			if r := pgoOnly(t, doc, "../reliant/cmd/reliant"); r.Action != pgoSkipped || !strings.Contains(r.Reason, tc.reason) {
				t.Errorf("result = %s %q, want skipped with %q", r.Action, r.Reason, tc.reason)
			}
			if len(f.local.params) != 0 {
				t.Error("a skipped binary was queried")
			}
		})
	}
}

// A committed profile is part of a ShellBuild's fingerprint when its pgo
// names it — `**/*.go` inputs do not select default.pgo — and a build that
// names none fingerprints exactly as before.
func TestShellBuildFingerprint_PGOProfile(t *testing.T) {
	projDir := t.TempDir()
	writeInput(t, projDir, "src/cmd/api/main.go", "package main")
	cwd := filepath.Join(projDir, "src")
	fp := func(svc WorkloadEntity) string {
		t.Helper()
		got, err := shellBuildFingerprint(svc, externalBuildSpec(svc, buildOptions{}, "dev", projDir), cwd, projDir, buildOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return got.Digest
	}
	plain := inputsSvc("api", "**/*.go")
	withPGO := inputsSvc("api", "**/*.go")
	withPGO.Build.Shell.PGO = map[string]ShellPGOConfig{"./cmd/api": {}}

	before, absent := fp(plain), fp(withPGO)
	if before == absent {
		t.Error("declaring pgo left the fingerprint unchanged")
	}
	writeInput(t, projDir, "src/cmd/api/default.pgo", "profile v1")
	v1 := fp(withPGO)
	writeInput(t, projDir, "src/cmd/api/default.pgo", "profile v2")
	v2 := fp(withPGO)
	if v1 == absent || v2 == v1 {
		t.Errorf("a written or refreshed profile left the fingerprint unchanged (absent %s, v1 %s, v2 %s)", absent, v1, v2)
	}
	if fp(withPGO) != v2 {
		t.Error("fingerprint is not stable")
	}
	if fp(plain) != before {
		t.Error("a build that names no pgo fingerprints the profile")
	}
	if got := shellBuildPGODigests(withPGO, projDir); len(got) != 1 || !strings.HasPrefix(got["src/cmd/api/default.pgo"], "sha256:") {
		t.Errorf("digests = %v", got)
	}
}
