package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// `forge release verify --json` tests.
//
// The property under test is not "the encoder works" — it is that the JSON
// mode and the text mode cannot disagree. Both render the same []
// artifactVerification through the same tally and the same verdict function,
// so every case here asserts the pair: the document's `ok` AND the error the
// command returns, which is what sets the process exit code.

// decodeVerifyReport runs the JSON path over a scripted fetcher and returns
// the parsed document alongside the error the command would have returned.
func decodeVerifyReport(t *testing.T, rel release.Release, f httpFetcher, strict bool) (releaseVerifyReport, error) {
	t.Helper()
	results := verifyReleaseArtifacts(context.Background(), f, rel, 4)

	var err error
	out := captureStdout(t, func() {
		err = emitReleaseVerifyJSON(rel, results, strict)
	})

	var report releaseVerifyReport
	if jsonErr := json.Unmarshal([]byte(out), &report); jsonErr != nil {
		t.Fatalf("decode report: %v\nraw: %s", jsonErr, out)
	}
	return report, err
}

// TestReleaseVerifyJSON_Verified is the clean release: every artifact checks
// out, `ok` is true, and the command returns no error (exit 0).
func TestReleaseVerifyJSON_Verified(t *testing.T) {
	const integrity = "sha512-good"
	rel := release.Release{
		Version: "v1.4.0",
		Git:     release.Git{Commit: "abc1234", Tag: "v1.4.0"},
		Artifacts: map[string]release.Artifact{
			"web-runtime": {Kind: release.KindNPM, Version: "1.0.0", Integrity: integrity},
		},
	}
	f := &stubFetcher{responses: []stubResponse{
		{match: "web-runtime", status: http.StatusOK, body: npmPackumentJSON(map[string]string{"1.0.0": integrity})},
	}}

	report, err := decodeVerifyReport(t, rel, f, false)
	if err != nil {
		t.Fatalf("verified release must not error: %v", err)
	}
	if !report.OK {
		t.Errorf("ok = false on a fully verified release: %+v", report.Summary)
	}
	if report.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", report.ExitCode)
	}
	if report.Release != "v1.4.0" {
		t.Errorf("release = %q, want v1.4.0", report.Release)
	}
	if len(report.Artifacts) != 1 || report.Artifacts[0].Name != "web-runtime" {
		t.Fatalf("artifacts = %+v", report.Artifacts)
	}
	// The status must be the lowercase string, not the iota. A consumer
	// reading `0` here would have to know the declaration order.
	if got := report.Artifacts[0].Status; got != verifyVerified {
		t.Errorf("status = %v, want verified", got)
	}
	if report.Summary.Verified != 1 {
		t.Errorf("summary = %+v, want 1 verified", report.Summary)
	}
}

// TestReleaseVerifyJSON_StatusIsLowercaseString pins the wire form itself. The
// point of MarshalJSON is that jq and a human read the same token; asserting
// through the typed round-trip alone would pass even if the iota leaked.
func TestReleaseVerifyJSON_StatusIsLowercaseString(t *testing.T) {
	cases := map[verifyStatus]string{
		verifyVerified:     `"verified"`,
		verifyFailed:       `"failed"`,
		verifyUnverifiable: `"unverifiable"`,
		verifyUnreachable:  `"unreachable"`,
	}
	for status, want := range cases {
		got, err := json.Marshal(status)
		if err != nil {
			t.Fatalf("marshal %v: %v", status, err)
		}
		if string(got) != want {
			t.Errorf("marshal %v = %s, want %s", status, got, want)
		}
		var back verifyStatus
		if err := json.Unmarshal(got, &back); err != nil {
			t.Fatalf("round-trip %s: %v", got, err)
		}
		if back != status {
			t.Errorf("round-trip %s = %v, want %v", got, back, status)
		}
	}

	// A label this build does not know must be REJECTED, not decoded. The
	// zero value is verifyVerified, so a lenient decoder would turn an
	// unreadable verdict into a passing one.
	var unknown verifyStatus
	if err := json.Unmarshal([]byte(`"probably-fine"`), &unknown); err == nil {
		t.Errorf("unknown status decoded as %v instead of erroring", unknown)
	}
}

// TestReleaseVerifyJSON_Failed: an artifact the registry does not have flips
// `ok` false and returns exit 1, with the failure individually visible rather
// than only aggregated into the summary.
func TestReleaseVerifyJSON_Failed(t *testing.T) {
	rel := release.Release{
		Version: "v1.4.0",
		Artifacts: map[string]release.Artifact{
			"unpublished": {Kind: release.KindNPM, Version: "2.0.0", Integrity: "sha512-never-shipped"},
		},
	}
	f := &stubFetcher{responses: []stubResponse{
		{match: "unpublished", status: http.StatusOK, body: npmPackumentJSON(map[string]string{"1.0.0": "sha512-x"})},
	}}

	report, err := decodeVerifyReport(t, rel, f, false)
	if err == nil {
		t.Fatal("a failed artifact must return a non-nil error so cobra exits non-zero")
	}
	if report.OK {
		t.Errorf("ok = true with %d failed artifact(s)", report.Summary.Failed)
	}
	if report.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", report.ExitCode)
	}
	if report.Summary.Failed != 1 {
		t.Errorf("summary = %+v, want 1 failed", report.Summary)
	}
	if report.Artifacts[0].Status != verifyFailed {
		t.Errorf("artifact status = %v, want failed", report.Artifacts[0].Status)
	}
	// The specific reason survives into JSON; a bare "failed" sends the
	// reader back to do the investigation the command just did.
	if report.Artifacts[0].Detail == "" {
		t.Error("detail is empty on a failed artifact")
	}
	if report.Diagnostic == "" {
		t.Error("diagnostic is empty on a failing report")
	}
}

// TestReleaseVerifyJSON_StrictFlipsOKForUnverifiable is the interaction that
// makes `ok` worth trusting. The SAME ledger, with the SAME unverifiable
// artifact, is ok=true without --strict and ok=false with it — because both
// route through the verdict text mode uses. The artifact's own status does not
// change: "could not check" is still not "failed".
func TestReleaseVerifyJSON_StrictFlipsOKForUnverifiable(t *testing.T) {
	// A file artifact has no publish URL recorded, so it is structurally
	// uncheckable.
	rel := release.Release{
		Version: "v1.4.0",
		Artifacts: map[string]release.Artifact{
			"cli-darwin-arm64": {Kind: release.KindFile, Version: "darwin-arm64", Integrity: sha("f")},
		},
	}

	lax, laxErr := decodeVerifyReport(t, rel, &stubFetcher{}, false)
	if laxErr != nil {
		t.Fatalf("without --strict an unverifiable artifact must not fail: %v", laxErr)
	}
	if !lax.OK {
		t.Errorf("ok = false without --strict: %+v", lax.Summary)
	}
	if lax.Summary.Unverifiable != 1 {
		t.Fatalf("summary = %+v, want 1 unverifiable", lax.Summary)
	}

	strict, strictErr := decodeVerifyReport(t, rel, &stubFetcher{}, true)
	if strictErr == nil {
		t.Fatal("--strict must return a non-nil error when something is unverifiable")
	}
	if strict.OK {
		t.Error("ok = true under --strict with an unverifiable artifact")
	}
	if strict.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", strict.ExitCode)
	}
	if !strict.Strict {
		t.Error("strict = false in a report produced with --strict")
	}

	// Unverifiable stays its own outcome in both runs — --strict changes the
	// verdict, not the finding.
	for _, r := range []releaseVerifyReport{lax, strict} {
		if r.Artifacts[0].Status != verifyUnverifiable {
			t.Errorf("artifact status = %v, want unverifiable", r.Artifacts[0].Status)
		}
		if r.Summary.Failed != 0 || r.Summary.Verified != 0 {
			t.Errorf("unverifiable leaked into verified/failed: %+v", r.Summary)
		}
	}
}

// TestReleaseVerifyJSON_CarriesGitProvenance: a release cut from a dirty tree
// ships bytes corresponding to no reviewable commit. No per-artifact check can
// see that, so the ledger's git block has to reach the JSON — text mode
// already prints it, and the machine-readable form must not be the weaker of
// the two.
func TestReleaseVerifyJSON_CarriesGitProvenance(t *testing.T) {
	const integrity = "sha512-good"
	rel := release.Release{
		Version: "v1.5.1",
		Git:     release.Git{Commit: "deadbeef", Tag: "v1.5.1", Dirty: true},
		Artifacts: map[string]release.Artifact{
			"pkg": {Kind: release.KindNPM, Version: "1.0.0", Integrity: integrity},
		},
	}
	f := &stubFetcher{responses: []stubResponse{
		{match: "pkg", status: http.StatusOK, body: npmPackumentJSON(map[string]string{"1.0.0": integrity})},
	}}

	report, err := decodeVerifyReport(t, rel, f, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Git.Commit != "deadbeef" || report.Git.Tag != "v1.5.1" {
		t.Errorf("git = %+v, want commit deadbeef tag v1.5.1", report.Git)
	}
	if !report.Git.Dirty {
		t.Error("git.dirty = false for a release cut from a dirty tree — the flag a downstream UI badges")
	}
	// Every artifact verified, so the release itself is ok; dirty is a
	// finding a consumer surfaces, not a verification failure.
	if !report.OK {
		t.Errorf("ok = false: %+v", report.Summary)
	}
}
