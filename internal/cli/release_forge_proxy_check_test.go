// File: internal/cli/release_forge_proxy_check_test.go
//
// Exercises the immutable-version gate in scripts/release-forge.sh (step 5).
//
// WHY THIS GATE EXISTS. proxy.golang.org is immutable: once it has served a
// version, that content is permanent. A tag that is published, deleted, and
// re-cut at a different commit is poisoned forever — the proxy keeps serving
// the FIRST tree and no action can correct it. pkg/v0.1.12 was burned exactly
// this way and had to be skipped. The local "tag already exists" check in
// step 4 cannot catch it, because the poisoning case is precisely the one
// where the local tag was deleted; only the proxy knows.
//
// The property most worth pinning is not the refusal but the FAIL-CLOSED
// behaviour: a guard that passes when the network is down is worse than no
// guard, because it manufactures confidence at the one moment the answer is
// unknown. TestReleaseForgeScript_ProxyUnreachableIsFatal is the test that
// matters here.
//
// These tests point the script at a local httptest server via
// FORGE_RELEASE_PROXY_BASE rather than the public proxy, so they are
// hermetic and safe in -short mode: every case refuses before reaching the
// script's slow `go mod download` step.
package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runForgeScriptWithProxy runs release-forge.sh against a fixture repo with
// the proxy base pointed at proxyBase.
func runForgeScriptWithProxy(t *testing.T, repo, proxyBase string, args ...string) (string, error) {
	t.Helper()
	script := releaseForgeScriptPath(t)
	full := append([]string{script, "--repo", repo}, args...)
	cmd := exec.CommandContext(context.Background(), "bash", full...)
	cmd.Env = append(os.Environ(), "FORGE_RELEASE_PROXY_BASE="+proxyBase,
		// The fixture has no prettier pin and these tests are about the
		// proxy gate; formatting the changelog is release_forge_changelog_test.go's.
		"FORGE_RELEASE_PRETTIER=true")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fixtureProxy serves .info responses from a per-path table. A path with no
// entry 404s, which is the proxy's own answer for an unpublished version.
func fixtureProxy(t *testing.T, infoByPath map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := infoByPath[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "not found: %s", r.URL.Path)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func infoJSON(version, hash string) string {
	return fmt.Sprintf(
		`{"Version":%q,"Time":"2026-09-10T03:38:53Z","Origin":{"VCS":"git","URL":"https://github.com/reliant-labs/forge","Hash":%q}}`,
		version, hash)
}

// TestReleaseForgeScript_RefusesVersionPublishedAtDifferentCommit is the
// pkg/v0.1.12 scenario reproduced: the version resolves on the proxy, but at
// a commit other than the one about to be tagged.
func TestReleaseForgeScript_RefusesVersionPublishedAtDifferentCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	// Both modules are covered independently: a half-finished earlier release
	// can burn either one alone, and either is fatal.
	for _, mod := range []string{
		"/github.com/reliant-labs/forge/@v/v0.2.0.info",
	} {
		t.Run(mod, func(t *testing.T) {
			repo := newForgeFixtureRepo(t)
			const publishedHash = "19dbc05d0000000000000000000000000000dead"
			proxy := fixtureProxy(t, map[string]string{mod: infoJSON("v0.2.0", publishedHash)})

			out, err := runForgeScriptWithProxy(t, repo, proxy, "--dry-run", "v0.2.0")
			if err == nil {
				t.Fatalf("expected refusal for an already-published version, got success:\n%s", out)
			}
			for _, want := range []string{
				"ALREADY PUBLISHED AT A DIFFERENT COMMIT",
				publishedHash,
				"proxy.golang.org is IMMUTABLE",
				"NOT a network problem",
				"Release a NEW version instead",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("refusal message missing %q:\n%s", want, out)
				}
			}
			// It must refuse BEFORE doing any release work — a tag that is
			// already burned should cost nothing to reject.
			if strings.Contains(out, "syncing version files") {
				t.Errorf("refused too late; the script had already begun editing:\n%s", out)
			}
		})
	}
}

// TestReleaseForgeScript_AllowsRepublishAtSameCommit keeps the gate from
// blocking a legitimate re-run: re-invoking after a successful push finds the
// version published at exactly the commit being tagged, which is not a
// conflict.
func TestReleaseForgeScript_AllowsRepublishAtSameCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("proceeds past the gate into a real go mod download")
	}
	repo := newForgeFixtureRepo(t)
	head := gitOut(t, repo, "rev-parse", "HEAD")
	proxy := fixtureProxy(t, map[string]string{
		"/github.com/reliant-labs/forge/@v/v0.2.0.info": infoJSON("v0.2.0", head),
	})

	out, err := runForgeScriptWithProxy(t, repo, proxy, "--dry-run", "v0.2.0")
	if err != nil {
		t.Fatalf("same-commit re-run should pass the gate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "idempotent") {
		t.Errorf("expected the gate to report an idempotent match:\n%s", out)
	}
}

// TestReleaseForgeScript_UnpublishedVersionPassesGate pins the ordinary path:
// a 404 from the proxy is an authoritative "this version is free".
func TestReleaseForgeScript_UnpublishedVersionPassesGate(t *testing.T) {
	if testing.Short() {
		t.Skip("proceeds past the gate into a real go mod download")
	}
	repo := newForgeFixtureRepo(t)
	proxy := fixtureProxy(t, nil) // every path 404s

	out, err := runForgeScriptWithProxy(t, repo, proxy, "--dry-run", "v0.2.0")
	if err != nil {
		t.Fatalf("unpublished version should pass the gate: %v\n%s", err, out)
	}
	for _, want := range []string{
		"github.com/reliant-labs/forge@v0.2.0: not published — free to use",

		"DRY RUN: all validations passed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestReleaseForgeScript_ProxyUnreachableIsFatal is the most important test in
// this file. An unreachable proxy and an unpublished version both produce an
// empty body; conflating them would let the guard pass at exactly the moment
// it cannot know the answer, which is worse than having no guard at all.
func TestReleaseForgeScript_ProxyUnreachableIsFatal(t *testing.T) {
	repo := newForgeFixtureRepo(t)
	// A port nothing listens on: curl fails to connect rather than 404ing.
	out, err := runForgeScriptWithProxy(t, repo, "http://127.0.0.1:1", "--dry-run", "v0.2.0")
	if err == nil {
		t.Fatalf("an unreachable proxy must NOT pass the check:\n%s", out)
	}
	for _, want := range []string{
		"could not reach the Go module proxy",
		"this check is NOT optional",
		"--skip-proxy-check",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("error message missing %q:\n%s", want, out)
		}
	}
	// The failure must not be mistakable for the "free to use" verdict.
	if strings.Contains(out, "free to use") {
		t.Errorf("a network failure reported the version as free:\n%s", out)
	}
}

// TestReleaseForgeScript_UnexpectedProxyStatusIsFatal covers a proxy that
// answers but not usefully (a 5xx, a captive portal). Like an unreachable
// proxy, it leaves the question unanswered, so it must stop the release.
func TestReleaseForgeScript_UnexpectedProxyStatusIsFatal(t *testing.T) {
	repo := newForgeFixtureRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "upstream is having a day")
	}))
	t.Cleanup(srv.Close)

	out, err := runForgeScriptWithProxy(t, repo, srv.URL, "--dry-run", "v0.2.0")
	if err == nil {
		t.Fatalf("a 500 from the proxy must NOT pass the check:\n%s", out)
	}
	if !strings.Contains(out, "returned HTTP 500") {
		t.Errorf("error message should name the status:\n%s", out)
	}
	if strings.Contains(out, "free to use") {
		t.Errorf("a 500 reported the version as free:\n%s", out)
	}
}

// TestReleaseForgeScript_SkipProxyCheckBypassesGate pins the escape hatch —
// and that it announces itself loudly, since taking it means accepting the
// risk the gate exists to remove.
func TestReleaseForgeScript_SkipProxyCheckBypassesGate(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	repo := newForgeFixtureRepo(t)
	// A proxy that would refuse if consulted; --skip-proxy-check must mean it
	// is never consulted at all.
	proxy := fixtureProxy(t, map[string]string{
		"/github.com/reliant-labs/forge/@v/v0.2.0.info": infoJSON("v0.2.0", "19dbc05d0000000000000000000000000000dead"),
	})

	out, _ := runForgeScriptWithProxy(t, repo, proxy, "--dry-run", "--skip-proxy-check", "v0.2.0")
	if strings.Contains(out, "ALREADY PUBLISHED") {
		t.Errorf("--skip-proxy-check still consulted the proxy:\n%s", out)
	}
	if !strings.Contains(out, "SKIPPING the immutable-version proxy check") {
		t.Errorf("the bypass must announce itself:\n%s", out)
	}
}
