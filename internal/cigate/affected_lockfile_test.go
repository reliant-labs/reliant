package cigate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/templates"
)

func TestWithLockfileChecks(t *testing.T) {
	pnpmFE := templates.FrontendCIConfig{Name: "web", Path: "frontends/web"}
	cases := []struct {
		name     string
		files    map[string]string
		changed  []string
		wantRows string
		wantArgv string // suffix of the lockfile row's argv
		wantDir  string
	}{
		{
			name:     "npm manifest changed",
			files:    map[string]string{"frontends/web/package.json": "{}", "frontends/web/package-lock.json": "{}"},
			changed:  []string{"frontends/web/package.json"},
			wantRows: "lockfile-web deps-web unit-tests",
			wantArgv: "ci --dry-run --offline --ignore-scripts --no-audit --no-fund",
			wantDir:  "frontends/web",
		},
		{
			name:     "yarn classic isolated install",
			files:    map[string]string{"frontends/web/package.json": "{}", "frontends/web/yarn.lock": "x"},
			changed:  []string{"frontends/web/yarn.lock"},
			wantRows: "lockfile-web deps-web unit-tests",
			wantArgv: "install --frozen-lockfile --ignore-scripts --non-interactive",
			wantDir:  "frontends/web",
		},
		{
			name:     "yarn berry isolated install",
			files:    map[string]string{"frontends/web/package.json": "{}", "frontends/web/yarn.lock": "x", "frontends/web/.yarnrc.yml": ""},
			changed:  []string{"frontends/web/yarn.lock"},
			wantRows: "lockfile-web deps-web unit-tests",
			wantArgv: "install --immutable --mode=skip-build",
			wantDir:  "frontends/web",
		},
		{
			name:     "pnpm lockfile changed",
			files:    map[string]string{"frontends/web/package.json": "{}", "frontends/web/pnpm-lock.yaml": "x"},
			changed:  []string{"frontends/web/pnpm-lock.yaml"},
			wantRows: "lockfile-web deps-web unit-tests",
			wantArgv: "install --frozen-lockfile --lockfile-only --ignore-scripts",
			wantDir:  "frontends/web",
		},
		{
			name:     "pnpm workspace root holds the lockfile",
			files:    map[string]string{"frontends/web/package.json": "{}", "pnpm-lock.yaml": "x", "pnpm-workspace.yaml": "x"},
			changed:  []string{"pnpm-workspace.yaml"},
			wantRows: "lockfile-web deps-web unit-tests",
			wantArgv: "install --frozen-lockfile --lockfile-only --ignore-scripts",
			wantDir:  "",
		},
		{
			name:     "source change only: N/A, no row",
			files:    map[string]string{"frontends/web/package.json": "{}", "frontends/web/package-lock.json": "{}"},
			changed:  []string{"frontends/web/src/App.tsx"},
			wantRows: "deps-web unit-tests",
		},
		{
			name:     "no lockfile: nothing to be out of sync with",
			files:    map[string]string{"frontends/web/package.json": "{}"},
			changed:  []string{"frontends/web/package.json"},
			wantRows: "deps-web unit-tests",
		},
		{
			name:     "full mode adds nothing",
			files:    map[string]string{"frontends/web/package.json": "{}", "frontends/web/package-lock.json": "{}"},
			wantRows: "deps-web unit-tests",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for f, body := range tc.files {
				write(t, root, f, body)
			}
			checks := []Check{{Name: "deps-web", Lane: LaneTest}, {Name: UnitTests, Lane: LaneTest}}
			got := WithLockfileChecks(checks, Affected{Changed: tc.changed}, root, []templates.FrontendCIConfig{pnpmFE})
			if strings.Join(names(got), " ") != tc.wantRows {
				t.Fatalf("rows = %v, want %s", names(got), tc.wantRows)
			}
			if tc.wantArgv == "" {
				return
			}
			c := find(t, got, "lockfile-web")
			if strings.HasPrefix(tc.name, "yarn") && (len(c.Argv) < 4 || c.Argv[0] != "node" || c.Argv[2] != isolatedYarnLockfileCheck) {
				t.Fatalf("Yarn must run in isolation: %v", c.Argv)
			}
			if argv := strings.Join(c.Argv, " "); !strings.HasSuffix(argv, tc.wantArgv) {
				t.Errorf("argv = %q, want suffix %q", argv, tc.wantArgv)
			}
			if c.Dir != tc.wantDir || c.Lane != LaneTest || c.Note == "" {
				t.Errorf("row = %+v", c)
			}
		})
	}
}

func TestYarnLockfileCheckIsIsolated(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	root := t.TempDir()
	write(t, root, "package.json", "{}")
	write(t, root, "packages/web/package.json", "{}")
	write(t, root, "node_modules/sentinel", "untouched")
	write(t, root, "yarn.lock", "original")
	// A fake Yarn attempts the same writes as an install, then fails. Both
	// its successful and failing paths must clean up without changing root.
	for _, status := range []string{"0", "1"} {
		script := `const fs=require('fs');
if(fs.existsSync('node_modules/sentinel')) throw Error('copied node_modules');
if(!fs.existsSync('packages/web/package.json')) throw Error('missing workspace');
if(process.env.YARN_ENABLE_SCRIPTS !== 'false') throw Error('scripts enabled');
fs.mkdirSync('node_modules'); fs.writeFileSync('node_modules/new', 'new');
fs.writeFileSync('yarn.lock', 'changed');
console.log(process.cwd());
process.exit(` + status + `);`
		cmd := exec.Command(node, "-e", isolatedYarnLockfileCheck, node, "-e", script)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if (err == nil) != (status == "0") {
			t.Fatalf("status %s: %v: %s", status, err, out)
		}
		tempWorkspace := strings.TrimSpace(string(out))
		if tempWorkspace == "" {
			t.Fatal("Yarn did not report its disposable workspace")
		}
		if _, err := os.Stat(tempWorkspace); !os.IsNotExist(err) {
			t.Fatalf("disposable workspace not removed: %v", err)
		}
		for file, want := range map[string]string{"yarn.lock": "original", "node_modules/sentinel": "untouched"} {
			body, err := os.ReadFile(filepath.Join(root, file))
			if err != nil || string(body) != want {
				t.Fatalf("checkout %s changed: %s, %v", file, body, err)
			}
		}
		if _, err := os.Stat(filepath.Join(root, "node_modules/new")); !os.IsNotExist(err) {
			t.Fatalf("install wrote to checkout: %v", err)
		}
	}
}

func TestNpmLockfileCheckOffline(t *testing.T) {
	if testing.Short() {
		t.Skip("real npm subprocesses; runs in task test")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm unavailable")
	}
	root := t.TempDir()
	write(t, root, "package.json", `{"name":"fixture","version":"1.0.0","dependencies":{"a":"1.0.0"}}`)
	write(t, root, "package-lock.json", `{"name":"fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"fixture","version":"1.0.0","dependencies":{"a":"1.0.0"}},"node_modules/a":{"version":"1.0.0","resolved":"https://registry.invalid/a/-/a-1.0.0.tgz","integrity":"sha512-YQ=="}}}`)
	write(t, root, "node_modules/sentinel", "untouched")
	for _, valid := range []bool{true, false} {
		if !valid {
			write(t, root, "package.json", `{"name":"fixture","version":"1.0.0","dependencies":{"a":"2.0.0"}}`)
		}
		cmd := exec.Command(npm, lockfileArgs(frontenddeps.Lock{Runner: "npm"})...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "npm_config_cache="+t.TempDir(), "npm_config_registry=http://127.0.0.1:1", "npm_config_update_notifier=false")
		out, err := cmd.CombinedOutput()
		if (err == nil) != valid {
			t.Fatalf("valid=%v: %v: %s", valid, err, out)
		}
	}
	body, err := os.ReadFile(filepath.Join(root, "node_modules/sentinel"))
	if err != nil || string(body) != "untouched" {
		t.Fatalf("npm touched node_modules: %s, %v", body, err)
	}
}

// A frontend whose deps row is not selected (its lane is not running) gets
// no lockfile row either.
func TestWithLockfileChecks_FollowsTheDepsRowsLane(t *testing.T) {
	root := t.TempDir()
	write(t, root, "frontends/web/package.json", "{}")
	write(t, root, "frontends/web/package-lock.json", "{}")
	checks := []Check{{Name: UnitTests, Lane: LaneTest}}
	got := WithLockfileChecks(checks, Affected{Changed: []string{"frontends/web/package.json"}}, root, []templates.FrontendCIConfig{webFE})
	if strings.Join(names(got), " ") != UnitTests {
		t.Fatalf("rows = %v", names(got))
	}
}
