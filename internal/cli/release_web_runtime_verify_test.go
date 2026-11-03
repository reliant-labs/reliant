// File: internal/cli/release_web_runtime_verify_test.go
//
// Exercises scripts/verify-npm-published.sh, the post-publish guard in
// .github/workflows/release-web-runtime.yml.
//
// WHY THESE TESTS EXIST. The guard used to be inline YAML, which meant it had
// never been run against a registry that refuses — and it was wrong. Its
// window was ~60s, on the assumption that npm propagation is near-instant.
// 0.3.2 published successfully and was not served for several minutes, so the
// guard failed a release that had worked, and said the artifact was missing
// when it was merely late.
//
// That is the failure mode worth pinning: a guard that cries wolf is not a
// harmless over-caution. It burns a release slot and it teaches people to
// disbelieve the one check that catches the real drift (a tag with no
// artifact, which is how web-runtime/v0.3.1 shipped).
//
// So the properties under test are the two that a human reading CI output
// depends on: the loop KEEPS WAITING past the old 60s ceiling, and a timeout
// names which of the two very different states it is in.
//
// Every case is hermetic and fast: NPM_VIEW_CMD points the script at a stub
// instead of the registry, and the window env vars shrink minutes to
// milliseconds. Nothing here touches the network.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func verifyNPMScriptPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p := filepath.Join(cwd, "..", "..", "scripts", "verify-npm-published.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("verify-npm-published.sh not found at %s: %v", p, err)
	}
	return p
}

// stubNPMView writes a fake `npm view` onto a temp dir and returns the
// command string to put in NPM_VIEW_CMD.
//
// succeedOnAttempt is 1-based: 1 serves immediately, 3 serves on the third
// probe (the propagation-delay case), and 0 never serves. The stub counts
// attempts in a file because each invocation is a fresh process.
func stubNPMView(t *testing.T, succeedOnAttempt int) string {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "attempts")
	script := filepath.Join(dir, "fake-npm-view.sh")
	body := fmt.Sprintf(`#!/usr/bin/env bash
# Args arrive as: <pkg@version> <field>
n=0
[ -f %[1]q ] && n="$(cat %[1]q)"
n=$((n + 1))
echo "$n" > %[1]q
want=%[2]d
if [ "$want" -ne 0 ] && [ "$n" -ge "$want" ]; then
  echo "stub-value"
  exit 0
fi
echo "npm error code E404" >&2
exit 1
`, counter, succeedOnAttempt)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub npm view: %v", err)
	}
	return "bash " + script
}

func runVerifyNPM(t *testing.T, viewCmd string, maxSeconds, firstDelay, maxDelay string, extraArgs ...string) (string, error) {
	t.Helper()
	args := append([]string{
		verifyNPMScriptPath(t),
		"--name", "@reliantlabs/forge-web-runtime",
		"--version", "0.3.2",
	}, extraArgs...)
	cmd := exec.CommandContext(t.Context(), "bash", args...)
	cmd.Env = append(os.Environ(),
		"NPM_VIEW_CMD="+viewCmd,
		"NPM_VERIFY_MAX_SECONDS="+maxSeconds,
		"NPM_VERIFY_FIRST_DELAY="+firstDelay,
		"NPM_VERIFY_MAX_DELAY="+maxDelay,
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestVerifyNPMPublished_ServedImmediatelyPasses is the happy path: the
// registry answers on the first probe and the script exits 0 without waiting.
func TestVerifyNPMPublished_ServedImmediatelyPasses(t *testing.T) {
	out, err := runVerifyNPM(t, stubNPMView(t, 1), "600", "5", "30")
	if err != nil {
		t.Fatalf("expected success, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "registry serves @reliantlabs/forge-web-runtime@0.3.2") {
		t.Errorf("output does not confirm the version is served:\n%s", out)
	}
}

// TestVerifyNPMPublished_KeepsWaitingPastTheOld60sCeiling is the 0.3.2
// regression, and the reason the window was widened.
//
// The stub refuses until the 8th probe. Under the old fixed 10s x 6 loop
// there was no 8th probe — the script gave up at six and failed a release
// that had in fact published. This asserts the version is still found.
func TestVerifyNPMPublished_KeepsWaitingPastTheOld60sCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script through real retry/backoff delays; runs in task test")
	}
	// A window wide enough for many probes, with the backoff pinned to 1s
	// so the test costs seconds rather than minutes. The point is the
	// NUMBER of attempts the loop is willing to make, not the wall-clock:
	// the old loop made exactly six and stopped.
	out, err := runVerifyNPM(t, stubNPMView(t, 8), "12", "1", "1", "--publish-reported-success")
	if err != nil {
		t.Fatalf("a version that appears on the 8th probe must still be found "+
			"(this is the 0.3.2 false-failure); got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "registry serves") {
		t.Errorf("output does not confirm the version is served:\n%s", out)
	}
}

// TestVerifyNPMPublished_TimeoutAfterSuccessfulPublishSaysNotYetServed pins
// the distinction the operator acts on.
//
// When the publish step reported success, a timeout means the artifact
// EXISTS and propagation is slow. Re-publishing is the wrong move — the
// version is immutable and the attempt is refused — so the message must not
// read as "missing artifact", and must not invite a re-publish.
func TestVerifyNPMPublished_TimeoutAfterSuccessfulPublishSaysNotYetServed(t *testing.T) {
	out, err := runVerifyNPM(t, stubNPMView(t, 0), "1", "1", "1", "--publish-reported-success")
	if err == nil {
		t.Fatalf("a never-served version must fail the check:\n%s", out)
	}
	if !strings.Contains(out, "PUBLISHED BUT IS NOT YET SERVED") {
		t.Errorf("a timeout after a SUCCESSFUL publish must be reported as a propagation delay, "+
			"not as a missing artifact:\n%s", out)
	}
	if !strings.Contains(out, "Do NOT re-publish") {
		t.Errorf("the message must warn against re-publishing an immutable version:\n%s", out)
	}
	// The wrong diagnosis must not also be present — a message carrying both
	// states tells the operator nothing.
	if strings.Contains(out, "is NOT PUBLISHED") {
		t.Errorf("message claims both 'not yet served' and 'not published':\n%s", out)
	}
}

// TestVerifyNPMPublished_TimeoutWithoutPublishSuccessSaysNotPublished is the
// original drift this guard was written for: a tag exists, nothing was
// published, and the registry has nothing to serve.
//
// Absent a success signal the script must assume the WORSE state. A guard
// that defaults to the reassuring diagnosis manufactures confidence at the
// one moment the answer is unknown.
func TestVerifyNPMPublished_TimeoutWithoutPublishSuccessSaysNotPublished(t *testing.T) {
	out, err := runVerifyNPM(t, stubNPMView(t, 0), "1", "1", "1")
	if err == nil {
		t.Fatalf("a never-served version must fail the check:\n%s", out)
	}
	if !strings.Contains(out, "is NOT PUBLISHED") {
		t.Errorf("without a publish success signal, a timeout must be reported as the "+
			"missing-artifact case:\n%s", out)
	}
	if strings.Contains(out, "PUBLISHED BUT IS NOT YET SERVED") {
		t.Errorf("must not claim the artifact exists when nothing reported publishing it:\n%s", out)
	}
}

// TestVerifyNPMPublished_DefaultWindowIsAtLeastTenMinutes pins the widened
// window against the file, so a future edit that reinstates a ~60s ceiling
// fails here rather than on a release day.
//
// Asserted on the default in the script rather than by waiting, because a
// test that actually slept for the window would cost ten minutes to prove a
// constant.
func TestVerifyNPMPublished_DefaultWindowIsAtLeastTenMinutes(t *testing.T) {
	body, err := os.ReadFile(verifyNPMScriptPath(t))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	if !strings.Contains(string(body), "NPM_VERIFY_MAX_SECONDS:-600") {
		t.Errorf("default verify window is no longer 600s — the 60s ceiling is what " +
			"failed the 0.3.2 release; widen deliberately and update this test")
	}
}

// TestReleaseWebRuntimeWorkflowUsesTheVerifyScript keeps the workflow and the
// tested script from drifting apart.
//
// The tests above prove the SCRIPT behaves. They prove nothing if the
// workflow quietly goes back to an inline loop, which is exactly how the
// untested version survived.
func TestReleaseWebRuntimeWorkflowUsesTheVerifyScript(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	path := filepath.Join(cwd, "..", "..", ".github", "workflows", "release-web-runtime.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release-web-runtime.yml: %v", err)
	}
	wf := string(body)
	if !strings.Contains(wf, "scripts/verify-npm-published.sh") {
		t.Errorf("the publish workflow no longer calls verify-npm-published.sh — " +
			"the verification is only tested while it lives in that script")
	}
	if !strings.Contains(wf, "--publish-reported-success") {
		t.Errorf("the workflow does not pass --publish-reported-success, so a timeout " +
			"will always be reported as the missing-artifact case even when the publish worked")
	}
}
