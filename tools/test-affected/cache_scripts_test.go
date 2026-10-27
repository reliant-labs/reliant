package main

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestRegeneratedSelectionPreparesNewDependencies(t *testing.T) {
	root := scriptFixture(t, "merge-gate.sh")
	b, err := os.ReadFile(filepath.Join(root, "scripts/merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	selection := s[strings.Index(s, "j() {"):strings.Index(s, "load_selection || exit 1")]
	deps := s[strings.Index(s, "prepare_dependencies() {"):strings.Index(s, "\nprepare_dependencies\n")]
	putFixture(t, root, "plan-2.json", `{"go":{"full":true,"packages":[],"deferred":[]},"web":{"full":true,"tests":[]},"electron":{"full":true},"forge_generate":{"full":true},"docs_generate":{"full":true}}`, 0644)
	// Exercise the actual selection/install functions, not a second implementation.
	body := "#!/usr/bin/env bash\nset -euo pipefail\ncd \"" + root + "\"\nrun_plan=plan-2.json\nbase=unused\nregen=0\nfail=0\ngit() { echo frontends/web/generated.ts; }\n" + selection + deps + "\nnpm_install() { echo \"$1\" >>deps; }\nload_selection\nprepare_dependencies\n[[ $gofull = true && $webfull = true && $elec = true && $dgen = true && $typecheck = true ]]\nfor d in . frontends/web electron tools/protoc-gen-es; do grep -Fx \"$d\" deps; done\n"
	putFixture(t, root, "scripts/selection.sh", body, 0755)
	if out, err := runFixture(root, "selection.sh", nil); err != nil {
		t.Fatalf("expanded plan selection/dependencies: %v %s", err, out)
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
