package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// captureStdoutAndStderr runs f with both streams redirected and returns what
// each received.
func captureStdoutAndStderr(t *testing.T, f func()) (stdout, stderr string) {
	t.Helper()
	read := func(r *os.File, into chan<- string) {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		into <- buf.String()
	}
	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outC, errC := make(chan string, 1), make(chan string, 1)
	go read(outR, outC)
	go read(errR, errC)
	os.Stdout, os.Stderr = outW, errW
	func() {
		defer func() { os.Stdout, os.Stderr = origOut, origErr }()
		f()
	}()
	_ = outW.Close()
	_ = errW.Close()
	return <-outC, <-errC
}

// requireOneJSONDocument fails unless stdout is exactly one JSON document.
func requireOneJSONDocument(t *testing.T, stdout string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not JSON (%v):\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout carries more than one document:\n%s", stdout)
	}
	return doc
}

// Prose printed while stdout is reserved goes to stderr; the document alone
// reaches stdout. Nested reservations keep the outermost real stdout.
func TestReserveStdoutForJSON_OnlyTheDocumentReachesStdout(t *testing.T) {
	stdout, stderr := captureStdoutAndStderr(t, func() {
		restore := reserveStdoutForJSON()
		fmt.Println("🔨 Running protoc-gen-forge (mode=descriptor) to extract proto metadata...")
		inner := reserveStdoutForJSON()
		fmt.Println("[registry] logging in to registry.test")
		inner()
		if err := emitJSONDocument(map[string]any{"ok": true}); err != nil {
			t.Error(err)
		}
		restore()
	})
	if doc := requireOneJSONDocument(t, stdout); doc["ok"] != true {
		t.Errorf("document = %v", doc)
	}
	for _, want := range []string{"protoc-gen-forge", "[registry] logging in"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr is missing %q:\n%s", want, stderr)
		}
	}
}

// THE REPORTED SHAPE, end to end: `forge env deploy <env> --plan-only --json`
// on a first run, where a registry login runs (a real docker subprocess that
// prints "Login Succeeded", which forge now keeps to itself) and forge prints
// descriptor progress. Stdout must still parse as exactly one JSON document
// (ea6e3b48, 8721487f), with the progress readable on stderr.
func TestDeployPlanOnlyJSON_StdoutIsOneDocumentOnAFirstRun(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the deploy path end to end and execs a fake docker; skipped in -short")
	}
	requirePOSIXFake(t, "docker")
	dir := deployAllProject(t)
	t.Chdir(dir)
	stubDeployBuild(t, dir, map[string]string{"api": sha("1")})
	_ = testLedger(t, dir)

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\ncat >/dev/null\necho 'WARNING! Your credentials are stored unencrypted'\necho 'Login Succeeded'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// What the reports saw on a first run: forge's descriptor progress and a
	// registry login whose docker subprocess inherits stdout. Both are
	// emitted here from a seam the command passes through; wherever in the
	// command such a line comes from, stdout must still carry only the
	// document.
	prov := captureReleaseProvenance
	captureReleaseProvenance = func(ctx context.Context, d string) release.Provenance {
		fmt.Println("🔨 Running protoc-gen-forge (mode=descriptor) to extract proto metadata...")
		fmt.Println("  ✅ gen/forge_descriptor/ generated")
		fmt.Printf("[registry] logging in to registry.test with env prod's control-plane credential\n")
		if err := runDockerLogin(ctx, "registry.test", platformRegistryUsername, "token"); err != nil {
			t.Error(err)
		}
		return prov(ctx, d)
	}
	t.Cleanup(func() { captureReleaseProvenance = prov })

	preflights := 0
	old := runPromoteClientDeploy
	runPromoteClientDeploy = func(_ context.Context, _ string, opts deployOptions) error {
		if !opts.preflightOnly {
			t.Error("--plan-only reached the apply")
			return nil
		}
		preflights++
		return nil
	}
	t.Cleanup(func() { runPromoteClientDeploy = old })

	f := deployCmdFlags{jsonOut: true}
	f.promote.planOnly = true
	f.promote.run.None = true
	var runErr error
	stdout, stderr := captureStdoutAndStderr(t, func() {
		runErr = dispatchDeployCmd(context.Background(), "prod", f)
	})
	if runErr != nil {
		t.Fatalf("--plan-only --json: %v\nstderr:\n%s", runErr, stderr)
	}
	requireOneJSONDocument(t, stdout)
	if preflights == 0 {
		t.Error("the plan ran no preflight")
	}
	if !strings.Contains(stderr, "protoc-gen-forge") {
		t.Errorf("stderr is missing forge's progress (it must stay readable):\n%s", stderr)
	}
	for _, bad := range []string{"Login Succeeded", "stored unencrypted"} {
		if strings.Contains(stdout, bad) || strings.Contains(stderr, bad) {
			t.Errorf("a successful docker login's output reached the user (%q):\nstdout:\n%s\nstderr:\n%s", bad, stdout, stderr)
		}
	}
}
