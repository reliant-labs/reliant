package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func scriptFixture(t *testing.T, name string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("real shell/compiler subprocesses; runs in full scoped tests")
	}
	root := t.TempDir()
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	putFixture(t, root, "scripts/"+name, string(b), 0755)
	return root
}
func putFixture(t *testing.T, root, name, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}
func runFixture(root, script string, env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts", script)}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

func TestGeneratorNeverReusesUnprovenRuntime(t *testing.T) {
	root := scriptFixture(t, "proto-generate-atomic.sh")
	putFixture(t, root, "template.yaml", "inputs:\n  - directory: contract\n", 0644)
	putFixture(t, root, "contract/input.proto", "contract-v1", 0644)
	putFixture(t, root, "plugin.js", "plugin-v1", 0644)
	buf := filepath.Join(root, "buf")
	generator := "#!/usr/bin/env bash\nset -euo pipefail\ncat template.yaml >/dev/null\ninput=$(cat contract/input.proto)\nplugin=$(cat plugin.js)\nwhile [ \"$1\" != -o ]; do shift; done\nmkdir -p \"$2/gen\"\nprintf '%s:%s:BUF-V1' \"$input\" \"$plugin\" >\"$2/gen/result\"\n"
	putFixture(t, root, "buf", generator, 0755)
	env := []string{"BUF=" + buf, "XDG_CACHE_HOME=" + filepath.Join(root, "cache")}
	run := func() {
		t.Helper()
		if out, err := runFixture(root, "proto-generate-atomic.sh", env, "template.yaml", "gen"); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	read := func() string {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(root, "gen/result"))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	run()
	original := read()
	// Same command path, pins and template, different executable bytes.
	putFixture(t, root, "buf", strings.ReplaceAll(generator, "BUF-V1", "BUF-V2"), 0755)
	run()
	if read() == original {
		t.Fatal("changed executable was skipped")
	}
	putFixture(t, root, "buf", generator, 0755)
	run()
	if read() != original {
		t.Fatal("restored executable reused fake output")
	}
	putFixture(t, root, "plugin.js", "plugin-v2", 0644)
	run()
	if !strings.Contains(read(), "plugin-v2") {
		t.Fatal("installed plugin edit was skipped")
	}
	putFixture(t, root, "gen/result", "hand edit", 0644)
	run()
	if read() == "hand edit" {
		t.Fatal("altered output was accepted")
	}
	before := read()
	if err := os.Remove(filepath.Join(root, "contract/input.proto")); err != nil {
		t.Fatal(err)
	}
	if out, err := runFixture(root, "proto-generate-atomic.sh", env, "template.yaml", "gen"); err == nil {
		t.Fatalf("failed input read accepted: %s", out)
	}
	if read() != before {
		t.Fatal("failure replaced existing output")
	}
}

// A hosted runner (16 GiB) ran out of memory with forge generate beside the
// other phases (reliant#1772 in the merge queue, three times): under the
// threshold it must run alone; on the 64 GiB dev host it stays concurrent.
func TestMergeGateRunsGenerateAloneOnASmallHost(t *testing.T) {
	root := scriptFixture(t, "merge-gate.sh")
	b, err := os.ReadFile(filepath.Join(root, "scripts/merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	start, end := "mem_kib=$(awk", "\n[ -n \"$base\" ]"
	_, rest, ok := strings.Cut(string(b), start)
	if !ok {
		t.Fatalf("merge-gate missing the memory budget %q", start)
	}
	block, _, ok := strings.Cut(rest, end)
	if !ok {
		t.Fatalf("merge-gate memory budget does not end at %q", end)
	}
	if !strings.Contains(string(b), `elif [ "$gen_alone" = 1 ]; then`) {
		t.Fatal("merge-gate computes gen_alone but never sequences generate on it")
	}
	// A generate that failed alone must not start docs, and whatever did start
	// is waited for even on failure: cleanup removes the scratch checkouts.
	for _, want := range []string{
		`if [ "$dgen" = true ] && [ "$fail" = 0 ]; then`,
		"\nfor p in \"${pids[@]}\"; do wait \"$p\" || fail=1; done\n",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("merge-gate must contain %q", want)
		}
	}
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{[]string{"MG_MEM_GIB=16"}, "1"},
		{[]string{"MG_MEM_GIB=64"}, "0"},
		{[]string{"MG_MEM_GIB=64", "MG_GEN_ALONE_GIB=128"}, "1"},
		{[]string{"MG_MEM_GIB=0"}, "0"}, // unknown size: the concurrent default
	} {
		cmd := exec.Command("bash", "-c", start+block+"\necho \"$gen_alone\"")
		cmd.Env = append(os.Environ(), tc.env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", tc.env, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != tc.want {
			t.Errorf("%v: gen_alone=%q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestRegeneratedSelectionPreparesNewDependencies(t *testing.T) {
	root := scriptFixture(t, "merge-gate.sh")
	b, err := os.ReadFile(filepath.Join(root, "scripts/merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// Extract the real functions, including npm_install: only package-manager
	// executables are faked, so no network or shared node_modules is touched.
	extract := func(start, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(string(b), start)
		if !ok {
			t.Fatalf("merge-gate missing fixture start %q", start)
		}
		body, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("merge-gate missing fixture end %q", end)
		}
		return start + body
	}
	selection := extract("j() {", "load_selection || exit 1")
	deps := extract("npm_install() {", "\nprepare_dependencies\n")
	full := LaneFull{Full: true}
	for _, tc := range []struct {
		name       string
		initial    Plan
		next       Plan
		changedWeb bool
		wantState  string // Go, web, Electron, docs, typecheck, forge, hooks-only, web test count
		wantDeps   []string
		failedDir  string
	}{
		{
			// A selected web lane type-checks too (#1768): vitest strips types.
			name:      "expanded web tests and full generation",
			next:      Plan{Go: GoPlan{LaneFull: full}, Web: WebPlan{Tests: []string{"generated.test.ts"}}, ForgeGen: ForgeGenPlan{LaneFull: full}},
			wantState: "true|false|false|false|true|true|false|1",
			wantDeps:  []string{"npm .", "npm tools/protoc-gen-es", "pnpm frontends/web"},
		},
		{
			name:      "expanded full web lane",
			next:      Plan{Web: WebPlan{LaneFull: full}},
			wantState: "false|true|false|false|true|false|false|0",
			wantDeps:  []string{"npm .", "pnpm frontends/web"},
		},
		{
			name:       "generated web source needs typecheck without tests",
			changedWeb: true,
			wantState:  "false|false|false|false|true|false|false|0",
			wantDeps:   []string{"npm .", "pnpm frontends/web"},
		},
		{
			name:      "hook-only generation needs no protoc install",
			next:      Plan{ForgeGen: ForgeGenPlan{LaneFull: full, HooksOnly: true}},
			wantState: "false|false|false|false|false|true|true|0",
		},
		{
			name:      "hook-only to full generation prepares protoc",
			initial:   Plan{ForgeGen: ForgeGenPlan{LaneFull: full, HooksOnly: true}},
			next:      Plan{ForgeGen: ForgeGenPlan{LaneFull: full}},
			wantState: "false|false|false|false|false|true|false|0",
			wantDeps:  []string{"npm tools/protoc-gen-es"},
		},
		{
			// Electron and docs run in the merge gate again (#1768). Electron's
			// tests need its node_modules (with its install script, which
			// fetches the binary); docs generation installs nothing.
			name:      "electron lane installs electron; docs installs nothing",
			next:      Plan{Electron: ElectronPlan{LaneFull: full}, DocGen: DocGenPlan{LaneFull: full}},
			wantState: "false|false|true|true|false|false|false|0",
			wantDeps:  []string{"npm .", "npm electron"},
		},
		{
			name:      "new dependency failure is propagated after waiting",
			next:      Plan{ForgeGen: ForgeGenPlan{LaneFull: full}},
			wantState: "false|false|false|false|false|true|false|0",
			wantDeps:  []string{"npm tools/protoc-gen-es"},
			failedDir: "tools/protoc-gen-es",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, plan := range map[string]Plan{"plan-1.json": tc.initial, "plan-2.json": tc.next} {
				data, err := json.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				putFixture(t, dir, name, string(data), 0644)
			}
			for _, d := range []string{".", "electron", "tools/protoc-gen-es"} {
				putFixture(t, dir, filepath.Join(d, "package-lock.json"), "{}", 0644)
			}
			putFixture(t, dir, "frontends/web/pnpm-lock.yaml", "lockfileVersion: '9.0'\n", 0644)
			// Assert the actual frozen-install commands, not just selected dirs.
			putFixture(t, dir, "bin/npm", `#!/usr/bin/env bash
set -euo pipefail
# electron keeps its install script (it fetches the binary); everything else ignores scripts.
if [[ $3 = electron ]]; then
  [[ $# = 6 && $1 = ci && $2 = --prefix && $4 = --no-audit && $5 = --no-fund && $6 = --loglevel=error ]]
else
  [[ $# = 7 && $1 = ci && $2 = --prefix && $4 = --ignore-scripts && $5 = --no-audit && $6 = --no-fund && $7 = --loglevel=error ]]
fi
printf 'npm %s\n' "$3" >>"$INSTALL_LOG"
[[ $3 != "$FAILED_DIR" ]]
`, 0755)
			putFixture(t, dir, "bin/pnpm", `#!/usr/bin/env bash
set -euo pipefail
[[ $# = 7 && $1 = install && $2 = --dir && $3 = frontends/web && $4 = --frozen-lockfile && $5 = --ignore-scripts && $6 = --config.confirmModulesPurge=false && $7 = --reporter=silent ]]
printf 'pnpm %s\n' "$3" >>"$INSTALL_LOG"
[[ $3 != "$FAILED_DIR" ]]
`, 0755)
			putFixture(t, dir, "changed-1", "", 0644)
			changed := ""
			if tc.changedWeb {
				changed = "frontends/web/generated.ts\n"
			}
			putFixture(t, dir, "changed-2", changed, 0644)
			// Invoke initial and regenerated selection/preparation in ONE shell:
			// stale flags and missing second-plan dependencies must both fail.
			wantFail := "0"
			if tc.failedDir != "" {
				wantFail = "1"
			}
			body := `#!/usr/bin/env bash
set -euo pipefail
base=unused
regen=0
fail=0
phase() { shift; "$@"; }
git() { cat "$changed_file"; }
` + selection + deps + `
changed_file=changed-1
run_plan=plan-1.json
load_selection
INSTALL_LOG="$PWD/initial-deps"
export INSTALL_LOG
prepare_dependencies
[[ $fail = 0 ]]
changed_file=changed-2
run_plan=plan-2.json
load_selection
INSTALL_LOG="$PWD/regenerated-deps"
prepare_dependencies
printf '%s|%s|%s|%s|%s|%s|%s|%s\n' "$gofull" "$webfull" "$elec" "$dgen" "$typecheck" "$fgen" "$hooks_only" "$nweb"
[[ $fail = ` + wantFail + ` ]]
`
			putFixture(t, dir, "scripts/selection.sh", body, 0755)
			env := []string{"PATH=" + filepath.Join(dir, "bin") + ":" + os.Getenv("PATH"), "FAILED_DIR=" + tc.failedDir}
			out, err := runFixture(dir, "selection.sh", env)
			if err != nil || strings.TrimSpace(string(out)) != tc.wantState {
				t.Fatalf("regenerated selection/dependencies: %v, state %q; want %q", err, out, tc.wantState)
			}
			readDeps := func(name string) []string {
				t.Helper()
				b, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				lines := strings.FieldsFunc(strings.TrimSpace(string(b)), func(r rune) bool { return r == '\n' })
				slices.Sort(lines) // Installs run concurrently; completion order is not contractual.
				return lines
			}
			if got := readDeps("initial-deps"); len(got) != 0 {
				t.Fatalf("initial selection installed unselected dependencies: %q", got)
			}
			if got := readDeps("regenerated-deps"); !slices.Equal(got, tc.wantDeps) {
				t.Fatalf("newly selected installs = %q; want exactly %q", got, tc.wantDeps)
			}
		})
	}
}

func TestTypecheckBackdatedCrossCheckoutAndHardlinks(t *testing.T) {
	if testing.Short() {
		t.Skip("real TypeScript compiler; runs in full scoped tests")
	}
	tsc := os.Getenv("TEST_TSC_BINARY")
	if tsc == "" {
		var err error
		tsc, err = exec.LookPath("tsc")
		if err != nil {
			t.Skip("set TEST_TSC_BINARY to run real TypeScript regression")
		}
	}
	a := scriptFixture(t, "web-typecheck.sh")
	b := scriptFixture(t, "web-typecheck.sh")
	r := &orderRepo{t: t}
	for _, root := range []string{a, b} {
		r.git(root, "init", "-q")
		putFixture(t, root, "frontends/web/tsconfig.json", `{"compilerOptions":{"composite":true,"declaration":true,"outDir":"node_modules/.tmp/tsc","tsBuildInfoFile":"node_modules/.tmp/state.tsbuildinfo"},"include":["src.ts"]}`, 0644)
		putFixture(t, root, "frontends/web/package.json", `{"scripts":{"typecheck":"`+tsc+` -b"}}`, 0644)
		putFixture(t, root, "frontends/web/src.ts", "export const n: number = 1;", 0644)
	}
	cache := filepath.Join(t.TempDir(), "shared")
	env := []string{"RELIANT_TSC_CACHE=" + cache}
	if out, err := runFixture(a, "web-typecheck.sh", env); err != nil {
		t.Fatalf("prime: %v %s", err, out)
	}
	src := filepath.Join(a, "frontends/web/node_modules/.tmp")
	dst := filepath.Join(b, "frontends/web/node_modules/.tmp")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-al", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("hardlink: %v %s", err, out)
	}
	before, err := os.ReadFile(filepath.Join(src, "state.tsbuildinfo"))
	if err != nil {
		t.Fatal(err)
	}
	putFixture(t, b, "frontends/web/src.ts", `export const n: number = "invalid";`, 0644)
	old := time.Unix(1, 0)
	if err := os.Chtimes(filepath.Join(b, "frontends/web/src.ts"), old, old); err != nil {
		t.Fatal(err)
	}
	// Both gates run with the same nominal shared cache and initially hardlinked
	// build state. B must report the bad source despite its older mtime.
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() { out, err := runFixture(a, "web-typecheck.sh", env); done <- result{out, err} }()
	out, badErr := runFixture(b, "web-typecheck.sh", env)
	good := <-done
	if good.err != nil {
		t.Fatalf("valid checkout: %v %s", good.err, good.out)
	}
	if badErr == nil || !strings.Contains(string(out), "TS2322") {
		t.Fatalf("backdated invalid source skipped: %v %s", badErr, out)
	}
	after, err := os.ReadFile(filepath.Join(src, "state.tsbuildinfo"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("other checkout's state was overwritten")
	}
}

// The second run in a checkout reuses its own incremental state (no --force)
// and still sees an edit made since.
func TestTypecheckReusesOwnStateAndSeesNewEdits(t *testing.T) {
	tsc := os.Getenv("TEST_TSC_BINARY")
	if tsc == "" {
		var err error
		if tsc, err = exec.LookPath("tsc"); err != nil {
			t.Skip("set TEST_TSC_BINARY to run real TypeScript regression")
		}
	}
	a := scriptFixture(t, "web-typecheck.sh")
	(&orderRepo{t: t}).git(a, "init", "-q")
	putFixture(t, a, "frontends/web/tsconfig.json", `{"compilerOptions":{"composite":true,"declaration":true,"outDir":"node_modules/.tmp/tsc","tsBuildInfoFile":"node_modules/.tmp/state.tsbuildinfo"},"include":["src.ts"]}`, 0644)
	putFixture(t, a, "frontends/web/package.json", `{"scripts":{"typecheck":"`+tsc+` -b"}}`, 0644)
	putFixture(t, a, "frontends/web/src.ts", "export const n: number = 1;", 0644)
	if out, err := runFixture(a, "web-typecheck.sh", nil); err != nil {
		t.Fatalf("first run: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(a, "frontends/web/node_modules/.tmp/.typecheck-owner")); err != nil {
		t.Fatalf("first run left no owner stamp: %v", err)
	}
	putFixture(t, a, "frontends/web/src.ts", `export const n: number = "invalid";`, 0644)
	if out, err := runFixture(a, "web-typecheck.sh", nil); err == nil || !strings.Contains(string(out), "TS2322") {
		t.Fatalf("incremental run missed a new error: %v %s", err, out)
	}
}
