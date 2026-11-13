package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func writeAdoptFixture(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The decision, with no scaffolding: what a claimable deploy does with a
// directory that has, or lacks, a forge.yaml.
func TestPlanClaimableAdoption(t *testing.T) {
	vite := `{"name":"a","devDependencies":{"vite":"^5.0.0"}}`
	next := `{"name":"a","dependencies":{"next":"15.0.0","react":"19.0.0"}}`
	for _, tc := range []struct {
		name      string
		files     map[string]string
		wantAdopt bool
		wantFlags string
		wantRefus string
	}{
		{"vite app", map[string]string{"package.json": vite}, true, "--in-place --kind site --name", ""},
		{"next app", map[string]string{"package.json": next}, true, "--in-place --kind site --name", ""},
		{"dockerfile app", map[string]string{"Dockerfile": "FROM scratch\n"}, true, "--in-place --kind app --claimable --name", ""},
		{"already a project", map[string]string{"forge.yaml": "name: x\n", "package.json": vite}, false, "", ""},
		{"go module", map[string]string{"go.mod": "module example.com/api\n"}, false, "", "is a Go module with no forge.yaml"},
		{"nothing", map[string]string{"README.md": "hi\n"}, false, "", "nothing to adopt"},
		{"package.json without vite or next", map[string]string{"package.json": `{"dependencies":{"express":"4"}}`}, false, "", "nothing to adopt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAdoptFixture(t, dir, tc.files)
			plan := planClaimableAdoption(dir, "")
			if plan.adopt != tc.wantAdopt {
				t.Fatalf("adopt = %v, want %v", plan.adopt, tc.wantAdopt)
			}
			if tc.wantAdopt {
				if got := strings.Join(plan.flags, " "); !strings.HasPrefix(got, tc.wantFlags) || !strings.HasSuffix(got, adoptionName(dir, "")) {
					t.Errorf("flags = %q, want %q … <dir name>", got, tc.wantFlags)
				}
				if got := strings.Join(plan.command(), " "); !strings.HasPrefix(got, "project new --in-place") {
					t.Errorf("command = %q", got)
				}
			}
			switch {
			case tc.wantRefus == "" && plan.refusal != nil:
				t.Fatalf("refused: %s", plan.refusal.msg)
			case tc.wantRefus != "":
				if plan.refusal == nil || !strings.Contains(plan.refusal.msg, tc.wantRefus) || plan.refusal.next == "" {
					t.Fatalf("refusal = %+v, want one containing %q and a next command", plan.refusal, tc.wantRefus)
				}
			}
		})
	}
	if got := planClaimableAdoption(t.TempDir(), "chosen"); got.refusal == nil || !strings.Contains(got.refusal.next, "project new chosen --kind site") {
		t.Errorf("an explicit name is not carried into the refusal's next command: %+v", got.refusal)
	}
}

// redirectHTTPTo sends every request the process makes through the default
// transport to srv, whatever host it names: the adopted prod env declares
// Reliant's real control plane, and no test reaches it.
func redirectHTTPTo(t *testing.T, srv *httptest.Server) {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	prev := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host, r.Host = target.Scheme, target.Host, target.Host
		return prev.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = prev })
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A fresh `npm create vite` app: `env deploy prod --claimable` adds the forge
// files, says so in one line, leaves the app's files alone, and goes on to the
// deploy.
func TestClaimableDeploy_AdoptsABareViteAppThenDeploys(t *testing.T) {
	testkit.Slow(t, "scaffolds a site project and runs the whole claimable deploy")
	fake, _ := claimableFixture(t)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	redirectHTTPTo(t, srv)
	prevLogin := dockerLogin
	dockerLogin = func(context.Context, []string, io.Reader) error { return nil }
	t.Cleanup(func() { dockerLogin = prevLogin })

	dir := filepath.Join(t.TempDir(), "my-landing")
	pkg := `{"name":"landing","private":true,"scripts":{"build":"vite build"},"devDependencies":{"vite":"^5.0.0"}}`
	index := "<h1>hi</h1>\n"
	writeAdoptFixture(t, dir, map[string]string{"package.json": pkg, "index.html": index})
	t.Chdir(dir)
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	fake.project, fake.env = "my-landing", "prod"

	out, err := runForge(t, "env", "deploy", "prod", "--claimable", "--no-wait", "--json")
	if err != nil {
		t.Fatalf("deploy after adoption failed: %v\n%s", err, out)
	}
	if fake.starts != 1 {
		t.Errorf("the deploy never reached the control plane: starts = %d\n%s", fake.starts, out)
	}
	for _, f := range []string{"forge.yaml", filepath.Join("deploy", "kcl", "prod", "main.k")} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("adoption did not write %s: %v", f, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "package.json")); string(got) != pkg {
		t.Errorf("adoption changed package.json:\n%s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "index.html")); string(got) != index {
		t.Errorf("adoption changed index.html:\n%s", got)
	}
}

// Human output: one line says what was added and that the app is untouched,
// and the scaffolder's own report is not printed above the deploy.
func TestAdoptBeforeClaimableDeploy_PrintsOneLine(t *testing.T) {
	testkit.Slow(t, "scaffolds a site project")
	dir := filepath.Join(t.TempDir(), "landing")
	writeAdoptFixture(t, dir, map[string]string{"package.json": `{"devDependencies":{"vite":"^5.0.0"}}`, "index.html": "<h1>hi</h1>\n", ".gitignore": "node_modules\n"})
	t.Chdir(dir)
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	resetCmdRoute(t)

	var out strings.Builder
	adopted, err := adoptBeforeClaimableDeploy(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(adopted, "forge.yaml") || !slices.Contains(adopted, "deploy/kcl/prod/main.k") || slices.Contains(adopted, "package.json") || slices.Contains(adopted, "index.html") {
		t.Errorf("the adopted paths name the app's files or miss forge's: %v", adopted)
	}
	got := strings.TrimRight(out.String(), "\n")
	if strings.Count(got, "\n") != 0 || !strings.Contains(got, "untouched") {
		t.Errorf("want one line saying the app is untouched, got:\n%s", got)
	}
	// It lists exactly what the adoption wrote: the files it added (a new
	// directory once, by name) and the .gitignore it appended to.
	for _, want := range []string{"added ", "AGENTS.md", "deploy/", "forge.yaml", "appended forge's entries to .gitignore"} {
		if !strings.Contains(got, want) {
			t.Errorf("the adoption line lacks %q:\n%s", want, got)
		}
	}
	for _, rel := range adopted {
		top, _, _ := strings.Cut(rel, "/")
		if top != rel {
			top += "/"
		}
		if top != ".gitignore" && !strings.Contains(got, top) {
			t.Errorf("the adoption wrote %s but the line does not name %s:\n%s", rel, top, got)
		}
	}
	var again strings.Builder
	if again2, err := adoptBeforeClaimableDeploy(context.Background(), &again); err != nil || again.Len() != 0 || len(again2) != 0 {
		t.Errorf("a second run adopts nothing and prints nothing: %q, %v", again.String(), err)
	}
}

// A Go module with no forge.yaml is refused with the MCP tool's message, writes
// nothing, and never reaches a control plane.
func TestClaimableDeploy_RefusesAGoModule(t *testing.T) {
	dir := t.TempDir()
	writeAdoptFixture(t, dir, map[string]string{"go.mod": "module example.com/api\n", "main.go": "package main\n"})
	t.Chdir(dir)
	t.Setenv("FORGE_HOME", t.TempDir())

	out, err := runForge(t, "env", "deploy", "prod", "--claimable")
	if err == nil {
		t.Fatalf("a Go module was not refused:\n%s", out)
	}
	for _, want := range []string{"is a Go module with no forge.yaml and no Dockerfile", "project new " + adoptionName(dir, "") + " --service api --claimable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, "forge.yaml")); statErr == nil {
		t.Error("a refusal wrote forge.yaml")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "deploy")); statErr == nil {
		t.Error("a refusal wrote deploy/")
	}
}

// Without --claimable nothing is adopted: the ordinary deploy still needs a
// project.
func TestDeploy_WithoutClaimableDoesNotAdopt(t *testing.T) {
	dir := t.TempDir()
	writeAdoptFixture(t, dir, map[string]string{"package.json": `{"devDependencies":{"vite":"^5.0.0"}}`, "index.html": "<h1>hi</h1>\n"})
	t.Chdir(dir)
	t.Setenv("FORGE_HOME", t.TempDir())

	if out, err := runForge(t, "env", "deploy", "prod", "--dry-run"); err == nil {
		t.Fatalf("a non-claimable deploy ran without a project:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "forge.yaml")); err == nil {
		t.Error("a non-claimable deploy adopted the app")
	}
}

// The cwd and stdio swap around the scaffolder is undone on every exit, so a
// failing adoption leaves the process where it found it.
func TestRunProjectNewQuietlyRestoresTheProcess(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	dir := t.TempDir()

	captured, err := runProjectNewQuietly(context.Background(), dir, []string{"--in-place", "--kind", "no-such-kind"})
	if err == nil {
		t.Fatalf("an unknown kind was accepted:\n%s", captured)
	}
	if now, _ := os.Getwd(); now != wd {
		t.Errorf("cwd = %q after the call, want %q", now, wd)
	}
	if os.Stdout != stdout || os.Stderr != stderr {
		t.Error("os.Stdout/os.Stderr were not restored")
	}
	if _, err := runProjectNewQuietly(context.Background(), filepath.Join(dir, "missing"), nil); err == nil {
		t.Error("a missing directory was accepted")
	}
	if now, _ := os.Getwd(); now != wd {
		t.Errorf("cwd = %q after a failed chdir, want %q", now, wd)
	}
}
