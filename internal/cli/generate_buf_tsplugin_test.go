package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// writeTSPluginBin creates an executable stub at dir/node_modules/.bin/protoc-gen-es.
func writeTSPluginBin(t *testing.T, dir string) {
	t.Helper()
	bin := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "protoc-gen-es"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The standalone layout: the frontend has its own node_modules, which is what
// every non-bridged project looks like. The frontend-local path must win.
func TestResolveLocalTSPluginRel_FrontendLocal(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	feDir := filepath.Join("frontends", "web")
	writeTSPluginBin(t, filepath.Join(projectDir, feDir))

	got, ok := resolveLocalTSPluginRel(projectDir, feDir)
	if !ok {
		t.Fatal("plugin installed in the frontend was not found")
	}
	if want := "./frontends/web/node_modules/.bin/protoc-gen-es"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveLocalTSPluginRel_WorkspaceHoisted is the reproduction.
//
// A dev build of forge writes a gitignored npm workspace root above the
// frontends (internal/generator/frontend_webruntime_devlink.go). npm then
// HOISTS every member's dependencies to <project>/node_modules and creates no
// frontends/<name>/node_modules at all — verified against a real scaffold.
//
// Looking only in the frontend made forge report "@bufbuild/protoc-gen-es not
// installed yet" for a plugin that WAS installed one directory up, skip the
// TypeScript pass, and emit no src/gen/ whatsoever. The failure then surfaced
// far away as tsc TS2307 "Cannot find module '@/gen/services/<svc>/v1/<svc>_pb'"
// against files forge had just claimed to generate — which reads as a codegen
// bug rather than a plugin that was never run.
func TestResolveLocalTSPluginRel_WorkspaceHoisted(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	writeTSPluginBin(t, projectDir) // hoisted to the workspace root

	got, ok := resolveLocalTSPluginRel(projectDir, filepath.Join("frontends", "web"))
	if !ok {
		t.Fatal("plugin hoisted to the workspace root was not found — forge skips TS codegen " +
			"and the scaffold fails tsc with TS2307 on its own generated imports")
	}
	if want := "./node_modules/.bin/protoc-gen-es"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Neither location has it: the caller must warn and skip rather than invoking
// buf and letting it die on a confusing fork/exec error.
func TestResolveLocalTSPluginRel_Absent(t *testing.T) {
	t.Parallel()

	if got, ok := resolveLocalTSPluginRel(t.TempDir(), filepath.Join("frontends", "web")); ok {
		t.Errorf("reported a plugin at %q when none is installed", got)
	}
}

const tsPluginBufGen = `version: v2
plugins:
  - local: ./frontends/web/node_modules/.bin/protoc-gen-es
    out: frontends/web/src/gen
    include_imports: true
    opt:
      - target=ts
`

// The plugin path is retargeted for THIS RUN ONLY — in the template handed to
// buf — never in the committed buf.gen.yaml.
//
// forge used to heal a mismatched path by rewriting the file on disk. The
// file is committed, and the path it wants depends on the generating
// machine's node_modules layout: a dev build of forge hoists node_modules to
// the project root (the web-runtime bridge), CI's `npm ci` puts it in the
// frontend. So a dev machine rewrote buf.gen.yaml to ./node_modules/…, that
// got committed, and CI's regenerate rewrote it straight back — a
// verify-generated failure caused by nothing but where npm put a directory
// (houndersclub, frontends/web/buf.gen.yaml).
func TestRetargetedTSTemplate_RetargetsInMemory(t *testing.T) {
	t.Parallel()

	got, changed := retargetedTSTemplate([]byte(tsPluginBufGen), "./node_modules/.bin/protoc-gen-es")
	if !changed {
		t.Fatal("a stale plugin path was reported as already correct")
	}
	if !strings.Contains(string(got), "- local: ./node_modules/.bin/protoc-gen-es") {
		t.Errorf("plugin path was not retargeted:\n%s", got)
	}
	if strings.Contains(string(got), "./frontends/web/node_modules/.bin/protoc-gen-es") {
		t.Errorf("the stale path survived:\n%s", got)
	}
	// Everything else the user owns must be untouched.
	for _, want := range []string{"out: frontends/web/src/gen", "include_imports: true", "- target=ts"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("retarget disturbed unrelated config (missing %q):\n%s", want, got)
		}
	}

	if _, changed := retargetedTSTemplate([]byte(tsPluginBufGen), "./frontends/web/node_modules/.bin/protoc-gen-es"); changed {
		t.Error("a path that already matches was reported as changed")
	}
}

// The end-to-end half: a TS generation pass on a machine whose node_modules is
// hoisted to the root leaves the committed buf.gen.yaml byte-identical, and
// hands buf the retargeted template instead.
func TestRunBufGenerateTypeScript_NeverRewritesCommittedBufGen(t *testing.T) {
	requirePOSIXFake(t, "buf")
	projectDir := t.TempDir()
	feRel := filepath.Join("frontends", "web")
	feBufGen := filepath.Join(projectDir, feRel, "buf.gen.yaml")
	if err := os.MkdirAll(filepath.Dir(feBufGen), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feBufGen, []byte(tsPluginBufGen), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTSPluginBin(t, projectDir) // hoisted: ONLY <project>/node_modules exists

	// A fake buf on PATH that records the --template it was given.
	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "buf-args")
	fakeBuf := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n--ARG--\\n' \"$a\"; done > " + argsFile + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "buf"), []byte(fakeBuf), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	fe := config.FrontendConfig{Name: "web", Type: "nextjs"}
	if err := runBufGenerateTypeScript(fe, &config.ProjectConfig{}, projectDir); err != nil {
		t.Fatalf("runBufGenerateTypeScript: %v", err)
	}

	after, err := os.ReadFile(feBufGen)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != tsPluginBufGen {
		t.Errorf("forge rewrote the committed buf.gen.yaml for this machine's node_modules layout:\n%s", after)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("buf was never invoked: %v", err)
	}
	if !strings.Contains(string(args), "- local: ./node_modules/.bin/protoc-gen-es") {
		t.Errorf("buf was not handed the retargeted plugin path; args:\n%s", args)
	}
}
