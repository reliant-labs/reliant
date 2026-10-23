// File: internal/cli/release_go_proxy_wait_test.go
//
// Exercises scripts/wait-for-go-proxy.sh, the post-push gate that stops a
// release commit from failing every downstream job for ~6 minutes.
//
// WHY THESE TESTS EXIST. `git push --atomic` returns as soon as GitHub has
// the tag; proxy.golang.org discovers it by polling, and sum.golang.org only
// records a hash after the proxy has fetched the tree. So there is a window —
// measured in minutes, not seconds — in which the tag exists and
// `go mod download <module>@<version>` 404s for the whole world. E2E Scaffold
// scaffolds a project that requires the new version and runs `go mod tidy`,
// so it fails on every single release commit for a release that is entirely
// correct.
//
// TestWaitForGoProxy_RaceUnwaitedResolutionFails is the reproduction: it
// models what the release flow does TODAY (resolve immediately after the
// push) and asserts that it fails. It is the test that must go red against
// current behaviour and green once the wait is in the flow.
//
// The other properties are the ones a human reading CI output depends on:
// the loop keeps waiting well past the first few probes, it does not declare
// success on the proxy alone while the checksum db is still empty, and a
// timeout names WHICH of the two very different states it is in — because
// "tagged but late" and "the tag never landed" have opposite remedies, and
// telling someone to re-cut a tag that is merely late burns the version
// permanently.
//
// Every case is hermetic: GOPROXY_WAIT_BASE / GOSUMDB_WAIT_BASE point at an
// httptest server and the window env vars shrink minutes to milliseconds.
// Nothing here touches the network.
package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	waitTestModule  = "github.com/reliant-labs/forge"
	waitTestVersion = "v0.1.30"
)

func waitForGoProxyScriptPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p := filepath.Join(cwd, "..", "..", "scripts", "wait-for-go-proxy.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("wait-for-go-proxy.sh not found at %s: %v", p, err)
	}
	return p
}

// ingestingProxy models the real propagation behaviour: the module proxy
// 404s until it has polled the origin, and the checksum database 404s until
// the proxy has fetched the tree.
//
// proxyReadyAfter / sumdbReadyAfter are 1-based probe counts on their own
// endpoint. 1 means "serves immediately"; 0 means "never".
type ingestingProxy struct {
	url         string
	proxyProbes atomic.Int64
	sumdbProbes atomic.Int64
}

func newIngestingProxy(t *testing.T, proxyReadyAfter, sumdbReadyAfter int) *ingestingProxy {
	t.Helper()
	ip := &ingestingProxy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/lookup/"):
			n := int(ip.sumdbProbes.Add(1))
			if sumdbReadyAfter != 0 && n >= sumdbReadyAfter {
				// A real sumdb lookup returns the module line plus tree
				// metadata; only the status code matters to the script.
				fmt.Fprintf(w, "%s %s h1:stub=\n", waitTestModule, waitTestVersion)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "not found")
		case strings.HasSuffix(path, ".info"):
			n := int(ip.proxyProbes.Add(1))
			if proxyReadyAfter != 0 && n >= proxyReadyAfter {
				fmt.Fprintf(w,
					`{"Version":%q,"Time":"2026-09-10T03:38:53Z"}`, waitTestVersion)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "not found")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	ip.url = srv.URL
	return ip
}

// runWaitForGoProxy invokes the script against the fixture endpoints with a
// window shrunk to milliseconds.
func runWaitForGoProxy(t *testing.T, base, maxSeconds, firstDelay, maxDelay string, extraArgs ...string) (string, error) {
	t.Helper()
	args := append([]string{
		waitForGoProxyScriptPath(t),
		"--module", waitTestModule,
		"--version", waitTestVersion,
	}, extraArgs...)
	cmd := exec.CommandContext(t.Context(), "bash", args...)
	cmd.Env = append(os.Environ(),
		"GOPROXY_WAIT_BASE="+base,
		"GOSUMDB_WAIT_BASE="+base,
		"GOPROXY_WAIT_MAX_SECONDS="+maxSeconds,
		"GOPROXY_WAIT_FIRST_DELAY="+firstDelay,
		"GOPROXY_WAIT_MAX_DELAY="+maxDelay,
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestWaitForGoProxy_RaceUnwaitedResolutionFails REPRODUCES THE BUG.
//
// It models the release flow as it behaves without this gate: the tag has
// just been pushed, and a dependent job immediately asks the proxy to resolve
// it. The fixture proxy behaves exactly as the real one does in that window —
// it has not polled the origin yet, so it 404s.
//
// The assertion is that the unwaited resolution FAILS. That is the ~6 minutes
// of red on every release commit, reproduced deterministically in
// milliseconds. The rest of this file asserts that waiting first turns it
// green.
func TestWaitForGoProxy_RaceUnwaitedResolutionFails(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script through real retry/backoff delays; runs in task test")
	}
	// Ingestion completes on the 4th probe; the unwaited job only ever makes
	// the 1st. (Real numbers: ~6 minutes vs. an immediate request.)
	ip := newIngestingProxy(t, 4, 1)

	// The unwaited path: one resolution attempt, no retry, no backoff —
	// which is what `go mod tidy` in E2E Scaffold amounts to.
	resp, err := http.Get(ip.url + "/" + waitTestModule + "/@v/" + waitTestVersion + ".info")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected the immediate post-push resolution to 404 — "+
			"if this passes, the fixture no longer models the ingestion delay "+
			"and the regression it guards is untested (got %d)", resp.StatusCode)
	}

	// Now the fix: waiting first makes the very same version resolvable.
	out, err := runWaitForGoProxy(t, ip.url, "60", "1", "1", "--tag-pushed")
	if err != nil {
		t.Fatalf("wait-for-go-proxy.sh should have waited out the ingestion delay, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "is ingested and resolvable") {
		t.Errorf("output does not confirm ingestion:\n%s", out)
	}

	// And it really did have to wait — a gate that passes on the first probe
	// would be indistinguishable from no gate at all.
	if got := ip.proxyProbes.Load(); got < 4 {
		t.Errorf("proxy was probed %d time(s); the fixture does not serve until the 4th, "+
			"so fewer than 4 means the script is not actually waiting", got)
	}
}

// TestWaitForGoProxy_ServedImmediatelyIsFast is the common case: a version
// already ingested costs one probe and no sleep.
func TestWaitForGoProxy_ServedImmediatelyIsFast(t *testing.T) {
	ip := newIngestingProxy(t, 1, 1)
	out, err := runWaitForGoProxy(t, ip.url, "600", "5", "30", "--tag-pushed")
	if err != nil {
		t.Fatalf("expected success, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "is ingested and resolvable") {
		t.Errorf("output does not confirm ingestion:\n%s", out)
	}
	if got := ip.proxyProbes.Load(); got != 1 {
		t.Errorf("proxy probed %d times, want exactly 1 — an already-ingested version must not sleep", got)
	}
}

// TestWaitForGoProxy_KeepsWaitingForSlowIngestion pins the window against the
// observed six-minute propagation. The fixture refuses until the 8th probe,
// which is far past what a naive "retry a couple of times" loop would make.
func TestWaitForGoProxy_KeepsWaitingForSlowIngestion(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script through real retry/backoff delays; runs in task test")
	}
	ip := newIngestingProxy(t, 8, 1)
	out, err := runWaitForGoProxy(t, ip.url, "60", "1", "1", "--tag-pushed")
	if err != nil {
		t.Fatalf("expected the loop to keep waiting to the 8th probe, got %v:\n%s", err, out)
	}
	if got := ip.proxyProbes.Load(); got < 8 {
		t.Errorf("proxy probed %d times, want at least 8", got)
	}
}

// TestWaitForGoProxy_WaitsForTheChecksumDatabaseToo is the half-done state:
// the proxy serves the version but the sumdb has no hash yet. A default
// `go mod tidy` still fails here, and it fails with a CHECKSUM MISMATCH-
// shaped error that reads as a security problem — sending whoever sees it to
// look in entirely the wrong place. So proxy-only must not count as success.
func TestWaitForGoProxy_WaitsForTheChecksumDatabaseToo(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script through real retry/backoff delays; runs in task test")
	}
	ip := newIngestingProxy(t, 1, 5)
	out, err := runWaitForGoProxy(t, ip.url, "60", "1", "1", "--tag-pushed")
	if err != nil {
		t.Fatalf("expected success once the sumdb caught up, got %v:\n%s", err, out)
	}
	if got := ip.sumdbProbes.Load(); got < 5 {
		t.Errorf("sumdb probed %d times, want at least 5 — the script declared success "+
			"before the checksum database had the version", got)
	}
	if !strings.Contains(out, "sum.golang.org has a checksum") {
		t.Errorf("output does not mention the checksum db:\n%s", out)
	}
}

// TestWaitForGoProxy_SkipSumdbStopsAtTheProxy covers the escape hatch for a
// module the checksum database never sees (a GOPRIVATE module). It must not
// probe the sumdb at all.
func TestWaitForGoProxy_SkipSumdbStopsAtTheProxy(t *testing.T) {
	ip := newIngestingProxy(t, 1, 0) // sumdb NEVER serves
	out, err := runWaitForGoProxy(t, ip.url, "10", "1", "1", "--tag-pushed", "--skip-sumdb")
	if err != nil {
		t.Fatalf("--skip-sumdb should succeed on the proxy alone, got %v:\n%s", err, out)
	}
	if got := ip.sumdbProbes.Load(); got != 0 {
		t.Errorf("sumdb probed %d times under --skip-sumdb, want 0", got)
	}
}

// TestWaitForGoProxy_TimeoutSaysTaggedButNotIngested is the message half, and
// it is the one that protects the version itself.
//
// When the push succeeded, a timeout means the proxy is merely late. The
// operator must NOT delete and re-cut the tag — proxy.golang.org is immutable,
// so re-cutting at a different commit burns the version permanently (see
// release-forge.sh step 5, and pkg/v0.1.12 which was burned exactly that way).
// The error text has to say so, because "my release is failing" plus an
// ambiguous message is precisely the situation in which someone re-cuts.
func TestWaitForGoProxy_TimeoutSaysTaggedButNotIngested(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script through real retry/backoff delays; runs in task test")
	}
	ip := newIngestingProxy(t, 0, 0) // never ingests
	out, err := runWaitForGoProxy(t, ip.url, "2", "1", "1", "--tag-pushed")
	if err == nil {
		t.Fatalf("expected a timeout failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "TAGGED BUT NOT YET INGESTED") {
		t.Errorf("timeout must name the tagged-but-late state:\n%s", out)
	}
	if !strings.Contains(out, "Do NOT delete and re-cut the tag") {
		t.Errorf("timeout must warn against re-cutting an immutable version:\n%s", out)
	}
}

// TestWaitForGoProxy_TimeoutWithoutTagPushedIsTheGraverCase is the default
// when no success signal was passed: assume the worse state. A tag that never
// landed is a real problem, and reporting it as mere propagation delay would
// hide it.
func TestWaitForGoProxy_TimeoutWithoutTagPushedIsTheGraverCase(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script through real retry/backoff delays; runs in task test")
	}
	ip := newIngestingProxy(t, 0, 0)
	out, err := runWaitForGoProxy(t, ip.url, "2", "1", "1")
	if err == nil {
		t.Fatalf("expected a timeout failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "IS NOT RESOLVABLE") {
		t.Errorf("without --tag-pushed a timeout must report the graver state:\n%s", out)
	}
	if strings.Contains(out, "TAGGED BUT NOT YET INGESTED") {
		t.Errorf("without a push success signal the script must not claim the tag exists:\n%s", out)
	}
}

// TestWaitForGoProxy_UnreachableEndpointIsNotMistakenForUningested guards the
// distinction the script's http_status helper exists for. An endpoint that
// cannot be reached is not the same fact as "the proxy says no such version",
// and it must not be reported as a successful ingestion.
func TestWaitForGoProxy_UnreachableEndpointIsNotMistakenForUningested(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script through real retry/backoff delays; runs in task test")
	}
	// A closed port: curl fails to connect rather than returning a status.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()

	out, err := runWaitForGoProxy(t, dead, "2", "1", "1", "--tag-pushed")
	if err == nil {
		t.Fatalf("an unreachable proxy must never report ingestion:\n%s", out)
	}
	if strings.Contains(out, "is ingested and resolvable") {
		t.Errorf("unreachable endpoint was treated as ingested:\n%s", out)
	}
}

// TestWaitForGoProxy_RequiresModuleAndVersion pins the argument contract: a
// missing argument must be a usage error, never a vacuous pass.
func TestWaitForGoProxy_RequiresModuleAndVersion(t *testing.T) {
	for _, args := range [][]string{
		{"--module", waitTestModule},
		{"--version", waitTestVersion},
		{},
	} {
		full := append([]string{waitForGoProxyScriptPath(t)}, args...)
		cmd := exec.CommandContext(t.Context(), "bash", full...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("args %v should be a usage error, got success:\n%s", args, out)
		}
	}
}
