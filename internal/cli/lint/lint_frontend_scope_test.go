package lint

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// All subprocesses here are tiny shell fakes over t.TempDir inputs, including
// the installer. No repo inputs are read by a child, no real npm/tsc is used,
// and no network is reachable through the tools. Keep this cache-sound test in
// the short tier: it pins execution, not merely post-execution finding filters.
func TestFrontendTypecheckScopeExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executables")
	}
	for _, tc := range []struct {
		name        string
		scope       []string
		want        []string
		custom      bool
		scan        bool
		installFail bool
		unlocated   bool
	}{
		{name: "backend", scope: []string{"internal/jobs"}},
		{name: "backend_and_proto", scope: []string{"internal/jobs", "proto/services/jobs"}},
		{name: "path_prefix_is_not_overlap", scope: []string{"frontends/web-tools"}},
		{name: "frontend", scope: []string{"frontends/web"}, want: []string{"web"}},
		{name: "frontend_file", scope: []string{"frontends/web/src/selected.ts"}, want: []string{"web"}},
		{name: "frontend_parent", scope: []string{"frontends"}, want: []string{"web", "admin"}},
		{name: "mixed", scope: []string{"internal/jobs", "frontends/web/src"}, want: []string{"web"}},
		{name: "unscoped", want: []string{"web", "admin"}},
		{name: "file_scope_install_failure", scope: []string{"frontends/web/src/selected.ts"}, want: []string{"web"}, installFail: true},
		{name: "file_scope_unlocated_error", scope: []string{"frontends/web/src/selected.ts"}, want: []string{"web"}, unlocated: true},
		{name: "custom_backend", scope: []string{"internal/jobs"}, custom: true},
		{name: "custom_frontend", scope: []string{"ui/console/src/selected.ts"}, want: []string{"web"}, custom: true},
		{name: "discovered_backend", scope: []string{"internal/jobs"}, scan: true},
		{name: "discovered_frontend", scope: []string{"frontends/web"}, want: []string{"web"}, scan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			inDir(t, root)
			t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
			calls := filepath.Join(root, "calls")
			t.Setenv("SCOPE_TOOL_CALLS", calls)
			bin := filepath.Join(root, "bin")
			mustMkdirAll(t, bin)
			// Only npm is selected by the production code. The fake install
			// creates the local compiler for the script-less fallback as well.
			npm := `#!/bin/sh
printf '%s|npm %s\n' "$PWD" "$*" >> "$SCOPE_TOOL_CALLS"
case "$1" in
ci|install)
  mkdir -p node_modules/.bin
  printf '%s\n' '#!/bin/sh' 'printf "%s|tsc %s\n" "$PWD" "$*" >> "$SCOPE_TOOL_CALLS"' 'echo "src/selected.ts(1,1): error TS2322: scope sentinel"' 'exit 2' > node_modules/.bin/tsc
  chmod +x node_modules/.bin/tsc
  exit 0 ;;
run)
  echo 'src/selected.ts(1,1): error TS2322: scope sentinel'
  exit 2 ;;
esac
exit 99
`
			if tc.installFail {
				npm = strings.Replace(npm, "ci|install)\n", "ci|install)\n  exit 1\n", 1)
			}
			if tc.unlocated {
				npm = strings.ReplaceAll(npm, "src/selected.ts(1,1): error TS2322: scope sentinel", "error TS18003: No inputs were found")
			}
			mustWrite(t, filepath.Join(bin, "npm"), npm)
			if err := os.Chmod(filepath.Join(bin, "npm"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			for _, dir := range []string{"internal/jobs", "proto/services/jobs", "frontends/web-tools"} {
				mustMkdirAll(t, filepath.Join(root, dir))
			}
			cfg := cfgWithFrontends("web", "admin")
			if tc.custom {
				cfg.Frontends[0] = cfg.Frontends[0].WithDir("ui/console")
			}
			dirs := map[string]string{}
			for _, fe := range cfg.Frontends {
				dir, ok := fe.Dir(".")
				if !ok {
					t.Fatal("invalid fixture frontend")
				}
				dirs[fe.Name] = dir
				var scripts map[string]string
				if fe.Name == "web" {
					scripts = map[string]string{"typecheck": "tsc --noEmit"}
				}
				writeFrontend(t, root, frontendFixture{name: fe.Name, dir: dir, scripts: scripts, tsconfig: true, tscExit: -1})
				mustWrite(t, filepath.Join(root, dir, "src", "selected.ts"), "const value: string = 1;\n")
				mustWrite(t, filepath.Join(root, dir, "package-lock.json"), "{}")
			}
			if tc.scan {
				cfg = nil // the no-config inventory fallback must also be scoped
			}
			scope, err := parseLintScope(root, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			// --install: scope must narrow which frontends an opted-in
			// install touches, not just which findings are reported.
			outcomes := collectLintOutcomesFrom(&lintRunCtx{ctx: context.Background(), cfg: cfg, cwd: root, scope: scope, install: true, installOut: io.Discard},
				[]linterStep{findStep(t, "frontend typecheck")})
			log := readLog(t, calls)
			for _, name := range []string{"web", "admin"} {
				want := false
				for _, selected := range tc.want {
					want = want || selected == name
				}
				prefix := filepath.Join(root, dirs[name]) + "|"
				installs := strings.Count(log, prefix+"npm ci\n")
				command := "npm run typecheck\n"
				if name == "admin" {
					command = "tsc --noEmit --pretty false\n"
				}
				runs := strings.Count(log, prefix+command)
				wantRuns := 1
				if tc.installFail {
					wantRuns = 0
				}
				if want && (installs != 1 || runs != wantRuns) || !want && (installs != 0 || runs != 0) {
					t.Errorf("%s: installs=%d checks=%d, selected=%v; calls:\n%s", name, installs, runs, want, log)
				}
				if !want && dirExists(filepath.Join(root, dirs[name], "node_modules")) {
					t.Errorf("unselected %s acquired node_modules", name)
				}
			}
			_, gated := flattenLintOutcomes(outcomes)
			if gated != (len(tc.want) > 0) {
				t.Errorf("gated=%v, want selected compiler errors to gate: %+v", gated, outcomes)
			}
			if len(tc.want) == 0 && outcomes[0].ran {
				t.Error("backend-only scope must report frontend typecheck as not run, not passed")
			}
		})
	}
}

// Scope limits tools, not backend analysis: a proto obligation still reads
// its handler across the boundary and gates when the required write is absent.
func TestFrontendScopePreservesBackendASTRules(t *testing.T) {
	for _, writes := range []bool{false, true} {
		t.Run(strconv.FormatBool(writes), func(t *testing.T) {
			handler := "package estimates\n"
			if writes {
				handler += "func derive(row *LineItem) { row.AmountCents = 1 }\n"
			}
			root := computedProject(t, lineItemProto, handler)
			inDir(t, root)
			scope, err := parseLintScope(root, []string{"proto/services/estimates"})
			if err != nil {
				t.Fatal(err)
			}
			outcomes := collectLintOutcomesFrom(&lintRunCtx{ctx: context.Background(), cwd: root, scope: scope},
				[]linterStep{findStep(t, "computed-fields lint"), findStep(t, "frontend typecheck")})
			_, gated := flattenLintOutcomes(outcomes)
			if gated == writes || !outcomes[0].ran || outcomes[1].ran {
				t.Fatalf("writes=%v: backend rule must run across scope without frontend tools: %+v", writes, outcomes)
			}
		})
	}
}

// Root-level static sites use path '.', so a scope below src still selects
// that frontend. Absolute paths must compare in the same coordinate system.
func TestFrontendTypecheckScopeRootAndAbsolute(t *testing.T) {
	root := t.TempDir()
	inDir(t, root)
	mustMkdirAll(t, filepath.Join(root, "src"))
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "site", Type: "vite"}.WithDir(".")}}
	scope, err := parseLintScope(root, []string{filepath.Join(root, "src")})
	if err != nil {
		t.Fatal(err)
	}
	rc := &lintRunCtx{cfg: cfg, cwd: root, scope: scope}
	if run, msg := findStep(t, "frontend typecheck").shouldRun(rc); !run {
		t.Fatalf("root frontend omitted for src scope: %s", msg)
	}
}
