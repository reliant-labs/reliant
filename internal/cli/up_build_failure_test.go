package cli

import (
	"io"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

const airFailedLog = `watching .
building...
# example.com/shop/cmd/shop
cmd/shop/main.go:12:2: undefined: frobnicate
failed to build, error: exit status 1
`

// TestLastBuildFailure pins TL-17's reading of an air log: the latest build
// cycle decides, and its compiler lines are what the error quotes.
func TestLastBuildFailure(t *testing.T) {
	got := lastBuildFailure(airFailedLog)
	for _, want := range []string{"undefined: frobnicate", "failed to build"} {
		if !strings.Contains(got, want) {
			t.Errorf("excerpt misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "watching") {
		t.Errorf("excerpt reaches before the failed build's start:\n%s", got)
	}
	// stop_on_error = false: air then tries the previous binary, which on a
	// first build does not exist. The failure stands.
	if got := lastBuildFailure(airFailedLog + "running...\nfailed to start tmp/shop, error: no such file\n"); !strings.Contains(got, "undefined: frobnicate") {
		t.Errorf("a failed build followed by air's run attempt was not reported:\n%s", got)
	}
	for name, log := range map[string]string{
		"a later build started": airFailedLog + "building...\n",
		"fixed and running":     airFailedLog + "building...\nrunning...\n",
		"still building":        "watching .\nbuilding...\n",
		"no air output":         "listening on :8080\n",
	} {
		if got := lastBuildFailure(log); got != "" {
			t.Errorf("%s: reported a build failure:\n%s", name, got)
		}
	}
}

// TestBuildFailureReadsTheServiceLog reads the failure out of the service's
// log under .forge/logs/<env>/, the file procRegistry.start writes.
func TestBuildFailureReadsTheServiceLog(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := upLogDir("dev")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := buildFailure("dev", "api"); got != "" {
		t.Fatalf("no log, yet a failure: %s", got)
	}
	// The real shape: the scaffolded .air.toml's build failing on a missing
	// `forge`, then air trying a binary that was never built.
	log := "building...\n/bin/sh: 1: forge: not found\nfailed to build, error: exit status 127\nrunning...\n" +
		"/bin/sh: 1: /p/tmp/shop: not found\nProcess Exit with Code: 127\n"
	if err := os.WriteFile(path.Join(dir, "api.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := buildFailure("dev", "api"); !strings.Contains(got, "forge: not found") {
		t.Errorf("the failure was not read:\n%s", got)
	}
}

// TestWaitHostReadinessFailsFastOnABuildFailure: the gate returns the
// compiler's lines at once instead of waiting out its timeout.
func TestWaitHostReadinessFailsFastOnABuildFailure(t *testing.T) {
	start := time.Now()
	err := waitHostReadiness(t.Context(), "dev", nil, time.Minute, 10*time.Millisecond, io.Discard, func() []hostReadyResult {
		return []hostReadyResult{{name: "api", port: 8080, state: portReadyNobody, buildFailed: lastBuildFailure(airFailedLog)}}
	})
	if err == nil {
		t.Fatal("readiness passed over a failed build")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("readiness waited %s; want an immediate failure", time.Since(start))
	}
	for _, want := range []string{"its build failed", "undefined: frobnicate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error misses %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "may still be in progress") {
		t.Errorf("error still guesses at progress:\n%v", err)
	}
}
