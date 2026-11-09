package pgtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  string
	}{
		{in: "16", want: "16.9.0"},
		{in: " 17 ", want: "17.5.0"},
		{in: "18", want: DefaultVersion},
		{in: "v16", want: "16.9.0"},
		{in: "16.4", want: "16.4.0"},
		{in: "16.4.0", want: "16.4.0"},
		{in: "9", wantErr: "supported majors: 18, 17, 16, 15, 14"},
		{in: "16.x", wantErr: "is not a major"},
		{in: "latest", wantErr: "is not a major"},
	}
	for _, c := range cases {
		got, err := ResolveVersion(c.in)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("ResolveVersion(%q) err = %v, want one containing %q", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ResolveVersion(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

// TestChooseVersion pins the resolution order: the env var, then the
// nearest forge.yaml at or above the working directory, then the default.
func TestChooseVersion(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "internal", "store")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	forgeYAML := filepath.Join(root, "forge.yaml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(forgeYAML, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("name: demo\nunknown_key: [what, ever]\ndatabase:\n  postgres_version: \"16\"\n")
	if c := chooseVersion("", pkg); c.err != nil || c.version != "16.9.0" || !c.declared || c.source != forgeYAML {
		t.Errorf("forge.yaml declaration from a package dir: %+v", c)
	}
	if c := chooseVersion("17", pkg); c.err != nil || c.version != "17.5.0" || c.source != EnvVersion {
		t.Errorf("env over forge.yaml: %+v", c)
	}

	write("name: demo\ndatabase:\n  postgres_version: \"9\"\n")
	if c := chooseVersion("", pkg); c.err == nil || !strings.Contains(c.err.Error(), forgeYAML) {
		t.Errorf("an unsupported declaration names its file: %+v", c)
	}

	write("name: demo\n")
	if c := chooseVersion("", pkg); c.err != nil || c.version != DefaultVersion || c.declared {
		t.Errorf("no declaration: %+v", c)
	}
}

// TestInstanceRootPerVersion: the default version keeps the root older
// forges share (so they reuse, and never reap, the same server); any other
// version is invisible to them.
func TestInstanceRootPerVersion(t *testing.T) {
	def := instanceRootFor(DefaultVersion)
	if filepath.Base(def) != "forge-pgtest" {
		t.Errorf("default root = %s, want …/forge-pgtest", def)
	}
	if v16 := instanceRootFor("16.9.0"); v16 == def || filepath.Base(v16) != "forge-pgtest-16.9.0" {
		t.Errorf("16 root = %s", v16)
	}
}

const versionHelperEnv = "PGTEST_VERSION_HELPER"

// TestEmbeddedServerRunsTheDeclaredMajor boots the embedded server in a child
// process (the version is fixed once per process) with
// FORGE_TEST_POSTGRES_VERSION=16 and asserts the server it got is postgres 16.
func TestEmbeddedServerRunsTheDeclaredMajor(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a postgres 16 embedded server in a child process (download on first run); runs in task test")
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestVersionHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(), versionHelperEnv+"=1", EnvVersion+"=16", EnvBaseURL+"=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "server_version=16.") {
		t.Fatalf("helper did not report a postgres 16 server:\n%s", out)
	}
}

// TestVersionHelperProcess is not a test: TestEmbeddedServerRunsTheDeclaredMajor
// runs it in a child.
func TestVersionHelperProcess(t *testing.T) {
	if os.Getenv(versionHelperEnv) == "" {
		t.Skip("helper process")
	}
	defer Shutdown()
	db, cleanup, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()
	var v string
	if err := db.QueryRow("SHOW server_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	t.Logf("server_version=%s", v)
}

// TestExternalServerMajorIsChecked: a declared version is held against the
// server FORGE_TEST_POSTGRES_URL names, so a service container on another
// major fails the run by name rather than passing it on the wrong postgres.
func TestExternalServerMajorIsChecked(t *testing.T) {
	if testing.Short() {
		t.Skip("boots the shared embedded postgres; runs in task test")
	}
	db, cleanup, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()
	var num int
	if err := db.QueryRow("SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		t.Fatal(err)
	}
	major := num / 10000
	ctx := t.Context()

	same := versionChoice{version: fmt.Sprintf("%d.0.0", major), declared: true, source: "forge.yaml"}
	if err := checkExternalVersion(ctx, db, same); err != nil {
		t.Errorf("same major: %v", err)
	}
	other := versionChoice{version: fmt.Sprintf("%d.0.0", major-1), declared: true, source: "forge.yaml"}
	if err := checkExternalVersion(ctx, db, other); err == nil || !strings.Contains(err.Error(), "forge.yaml declares postgres") {
		t.Errorf("other major: err = %v", err)
	}
	if err := checkExternalVersion(ctx, db, versionChoice{version: DefaultVersion}); err != nil {
		t.Errorf("an undeclared default is not checked: %v", err)
	}
}
