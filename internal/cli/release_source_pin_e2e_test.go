package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// THE SIBLING-CHECKOUT RELEASE GUARD, end to end through the real command.
//
// Prod release 20261007.165315 shipped an Oct-4 reliant image although
// control-plane's go.mod pinned a newer reliant commit: the reliant image is a
// ShellBuild whose cwd is the sibling checkout (`cwd = "../reliant"`), and the
// command built whatever that checkout had on disk. Nothing compared the
// checkout with the pin, so a release recorded the wrong bytes as though they
// were the pinned ones.
//
// These tests build a project whose go.mod pins a sibling module at commit A
// and point a ShellBuild at that sibling, then run
// `forge env build <env> --release`. A sibling at any other commit, or with
// uncommitted changes, must be refused BEFORE the command runs — the marker
// file the command writes proves it never started.

// sourceGuardFixture is a project directory and its sibling checkout.
type sourceGuardFixture struct {
	project string
	sibling string
	// pinned is the sibling commit the project's go.mod requires; next is a
	// later commit on the same branch.
	pinned, next string
	// marker is the file the ShellBuild writes: the sibling HEAD it ran at.
	marker string
}

// newSourceGuardFixture lays out <root>/acme (the project) and <root>/sib
// (its sibling checkout, two commits, HEAD at the second). go.mod pins the
// FIRST commit as a pseudo-version, the shape `go get <module>@<sha>` writes.
func newSourceGuardFixture(t *testing.T) sourceGuardFixture {
	t.Helper()
	root := t.TempDir()
	f := sourceGuardFixture{
		project: filepath.Join(root, "acme"),
		sibling: filepath.Join(root, "sib"),
	}
	f.marker = filepath.Join(f.project, "built-from.txt")

	initGitRepo(t, f.sibling, map[string]string{
		"go.mod":    "module example.com/sib\n\ngo 1.22\n",
		"README.md": "one\n",
	})
	gitIn(t, f.sibling, "remote", "add", "origin", "https://github.com/example/sib.git")
	f.pinned = gitOut(t, f.sibling, "rev-parse", "HEAD")
	writeStateFile(t, f.sibling, "README.md", "two\n")
	gitIn(t, f.sibling, "commit", "--quiet", "-am", "next")
	f.next = gitOut(t, f.sibling, "rev-parse", "HEAD")

	mainK := `import file
import forge
import forge.workloads as fw

_marker = file.workdir() + "/built-from.txt"

output = forge.render(forge.Bundle {
    project = "sgacme"
    workloads = [fw.Workload {
        name = "sib"
        kind = "tool"
        image = "registry.example.com/acme/sib"
        runtime = forge.BuildOnly {}
        build = forge.ShellBuild {
            cwd = "../sib"
            cmd = "git rev-parse HEAD > " + _marker
        }
    }]
})
`
	initGitRepo(t, f.project, map[string]string{
		"forge.yaml": "name: sgacme\nmodule_path: github.com/example/acme\n",
		"go.mod": "module github.com/example/acme\n\ngo 1.22\n\n" +
			"require example.com/sib v0.0.0-20260101000000-" + f.pinned[:12] + "\n",
		".gitignore":            ".forge/\nbin/\nbuilt-from.txt\n",
		"deploy/kcl/kcl.mod":    "[package]\nname = \"sgacme_deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/rel/main.k": mainK,
	})
	markServiceProject(t, f.project)
	return f
}

// enterSourceGuardFixture makes the fixture the working project, with a
// private machine ledger and the registry stubbed: the ShellBuild's "push"
// resolves to a fixed digest, so a release can be cut with no registry on the
// machine.
func enterSourceGuardFixture(t *testing.T, f sourceGuardFixture) {
	t.Helper()
	t.Chdir(f.project)
	useLedgerHome(t, t.TempDir())
	prev := externalImageDigestResolver
	externalImageDigestResolver = func(context.Context, string) (string, []string, error) {
		return "sha256:" + strings.Repeat("5a", 32), []string{"linux/amd64"}, nil
	}
	t.Cleanup(func() { externalImageDigestResolver = prev })
}

// runSourceGuardRelease runs `forge env build rel --release <version>` in the
// fixture.
func runSourceGuardRelease(t *testing.T, f sourceGuardFixture, version string) (string, error) {
	t.Helper()
	enterSourceGuardFixture(t, f)
	return runForge(t, "env", "build", "rel", "--release", version)
}

// assertNotBuilt fails when the ShellBuild ran: a refusal that comes after the
// build has already pushed images under the release's tag.
func assertNotBuilt(t *testing.T, f sourceGuardFixture) {
	t.Helper()
	if got, err := os.ReadFile(f.marker); err == nil {
		t.Errorf("the ShellBuild ran (it built sibling HEAD %s) — the guard must refuse before any build starts",
			strings.TrimSpace(string(got)))
	}
}

func TestReleaseRefusesSiblingCheckoutOffItsPin(t *testing.T) {
	if testing.Short() {
		t.Skip("creates git repos, renders KCL and runs the whole CLI; runs in task test")
	}
	f := newSourceGuardFixture(t)

	out, err := runSourceGuardRelease(t, f, "v1")
	if err == nil {
		built, _ := os.ReadFile(f.marker)
		t.Fatalf("release v1 was cut although ../sib is at %s and go.mod pins %s.\n"+
			"  The ShellBuild built: %s\n  output:\n%s",
			f.next[:12], f.pinned[:12], strings.TrimSpace(string(built)), out)
	}
	msg := err.Error()
	t.Logf("refusal:\n%s", msg)
	for _, want := range []string{
		f.next[:12],   // what the checkout is at
		f.pinned[:12], // what go.mod pins
		"example.com/sib", "go.mod",
		"checkout --detach " + f.pinned[:12], // the literal fix
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, msg)
		}
	}
	assertNotBuilt(t, f)
}

func TestReleaseRefusesDirtySiblingCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("creates git repos, renders KCL and runs the whole CLI; runs in task test")
	}
	f := newSourceGuardFixture(t)
	// The sibling is ON its pin, with an uncommitted edit: the bytes built
	// would be the pin plus a change no commit records.
	gitIn(t, f.sibling, "checkout", "--quiet", "--detach", f.pinned)
	writeStateFile(t, f.sibling, "README.md", "local edit\n")

	_, err := runSourceGuardRelease(t, f, "v1")
	if err == nil {
		t.Fatalf("release v1 was cut from a sibling checkout with uncommitted changes")
	}
	msg := err.Error()
	t.Logf("refusal:\n%s", msg)
	for _, want := range []string{"uncommitted", "stash push --include-untracked"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, msg)
		}
	}
	assertNotBuilt(t, f)
}

// A sibling AT its pin, clean, releases — and both the build state and the
// release ledger record the commit the image was built from.
func TestReleaseRecordsSiblingCheckoutAtItsPin(t *testing.T) {
	if testing.Short() {
		t.Skip("creates git repos, renders KCL and runs the whole CLI; runs in task test")
	}
	f := newSourceGuardFixture(t)
	gitIn(t, f.sibling, "checkout", "--quiet", "--detach", f.pinned)

	if out, err := runSourceGuardRelease(t, f, "v1"); err != nil {
		t.Fatalf("a sibling at its pin must release: %v\n%s", err, out)
	}
	built, err := os.ReadFile(f.marker)
	if err != nil || strings.TrimSpace(string(built)) != f.pinned {
		t.Fatalf("the ShellBuild built %q (err %v), want the pinned %s", strings.TrimSpace(string(built)), err, f.pinned)
	}

	st, err := buildtarget.ReadState(f.project, "rel", "sib")
	if err != nil || st == nil || st.Source == nil {
		t.Fatalf("build state records no source checkout: state=%+v err=%v", st, err)
	}
	want := buildtarget.Source{
		Dir: st.Source.Dir, Repo: "github.com/example/sib", Module: "example.com/sib", ModuleDir: ".",
		Commit: f.pinned, Dirty: false,
	}
	if *st.Source != want {
		t.Errorf("build state source = %+v, want %+v", *st.Source, want)
	}

	ledger, err := machineLedger(f.project)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := ledger.Releases.Get(context.Background(), "v1")
	if err != nil || rel == nil {
		t.Fatalf("release v1 not in the ledger: %v", err)
	}
	art, ok := rel.Artifacts["registry.example.com/acme/sib"]
	if !ok {
		t.Fatalf("release v1 has no sib artifact: %+v", rel.Artifacts)
	}
	wantFrom := release.BuildSource{Repo: "github.com/example/sib", Commit: f.pinned}
	if art.BuiltFrom == nil || *art.BuiltFrom != wantFrom {
		t.Errorf("ledger artifact built_from = %+v, want %+v", art.BuiltFrom, wantFrom)
	}
}

// A CUT-ONLY release (`--release --no-build`) has no live checkout to read: it
// is held to the checkout the earlier push RECORDED. A push from an off-pin
// sibling is allowed — it is not a release — but cutting a release over it
// is not.
func TestReleaseCutOnlyRefusesRecordedOffPinSibling(t *testing.T) {
	if testing.Short() {
		t.Skip("creates git repos, renders KCL and runs the whole CLI; runs in task test")
	}
	f := newSourceGuardFixture(t)
	enterSourceGuardFixture(t, f)

	if out, err := runForge(t, "env", "build", "rel", "--push", "--tag", "t1"); err != nil {
		t.Fatalf("a push is not a release and must not be refused: %v\n%s", err, out)
	}
	// The sibling moves back onto its pin AFTER the build: the cut must judge
	// what was built, not what is checked out now.
	gitIn(t, f.sibling, "checkout", "--quiet", "--detach", f.pinned)

	_, err := runForge(t, "env", "build", "rel", "--release", "v1", "--no-build")
	if err == nil {
		t.Fatalf("release v1 was cut over a build of %s while go.mod pins %s", f.next[:12], f.pinned[:12])
	}
	t.Logf("refusal:\n%s", err)
	// The images are already built from the wrong checkout, so the remedy is
	// a rebuild — re-running this --no-build cut would refuse again.
	for _, want := range []string{f.next[:12], f.pinned[:12], "was not recorded", "built from:", "forge env build rel --release v1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%s", want, err)
		}
	}
	ledger, lerr := machineLedger(f.project)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if rel, _ := ledger.Releases.Get(context.Background(), "v1"); rel != nil {
		t.Errorf("a refused cut recorded release v1: %+v", rel)
	}
}

// `--plan` preflights exactly what the real build refuses.
func TestReleasePlanRefusesSiblingCheckoutOffItsPin(t *testing.T) {
	if testing.Short() {
		t.Skip("creates git repos, renders KCL and runs the whole CLI; runs in task test")
	}
	f := newSourceGuardFixture(t)
	enterSourceGuardFixture(t, f)

	out, err := runForge(t, "env", "build", "rel", "--release", "v1", "--plan")
	if err == nil {
		t.Fatalf("--plan passed a release whose sibling is off its pin:\n%s", out)
	}
	if !strings.Contains(err.Error(), "checkout --detach "+f.pinned[:12]) {
		t.Errorf("plan refusal does not carry the fix:\n%s", err)
	}
	assertNotBuilt(t, f)
}
