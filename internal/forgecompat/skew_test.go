package forgecompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDecideRefusesBinaryBehindPin is the regression this file exists for.
//
// A control-plane pinned to forge v0.1.43 was generated with an installed
// v0.1.42 binary. forge's KCL schema module is EMBEDDED in the binary, so the
// older binary carried the older schema — and the render failed on a field
// that only exists in v0.1.43 with KCL's own diagnostic, pointing at
// `deploy/kcl/dev/main.k:2933`. That message blames the project's KCL for a
// field the project is entitled to use, and names neither version.
//
// Decide used to wave this through: it only refused when the project was
// OLDER than the binary (StalePin), and returned OK for a newer pin on the
// grounds that "older templates calling into a newer library is the ordinary
// upgrade order". That reasoning holds for the Go library half and NOT for
// the KCL half, because the schema does not come from the project's module
// graph — it comes from the binary.
func TestDecideRefusesBinaryBehindPin(t *testing.T) {
	// Decide honours the operator override; a developer shell that sets it
	// (Reliant does) must not change these verdicts.
	t.Setenv(SkewOverrideEnv, "")
	if got := Decide("v0.1.42", "v0.1.42", "v0.1.43", false); got != BehindPin {
		t.Errorf("Decide(binary v0.1.42, pin v0.1.43) = %v, want BehindPin", got)
	}
	// A minor-version gap is the same defect, further apart.
	if got := Decide("v0.1.40", "v0.1.40", "v0.2.0", false); got != BehindPin {
		t.Errorf("Decide(binary v0.1.40, pin v0.2.0) = %v, want BehindPin", got)
	}
	// Equal is not skew.
	if got := Decide("v0.1.43", "v0.1.43", "v0.1.43", false); got != OK {
		t.Errorf("Decide(equal versions) = %v, want OK", got)
	}
	// A bridged project compiles against source: there is no version to be
	// behind, and whoever wired the bridge owns that checkout's coherence.
	if got := Decide("v0.1.42", "v0.1.42", "", true); got != OK {
		t.Errorf("Decide(bridged) = %v, want OK", got)
	}
}

// TestSkewDiagnosisNamesBothVersionsAndTheInstall is the message contract.
//
// Each assertion is one thing KCL's own schema error lacked. The agent that
// hit this spent real time reading `main.k:2933` as the fault, because
// nothing in the output mentioned that two forge versions were in play.
func TestSkewDiagnosisNamesBothVersionsAndTheInstall(t *testing.T) {
	msg := SkewDiagnosis("v0.1.43", "v0.1.42")
	if msg == "" {
		t.Fatal("SkewDiagnosis said nothing about a binary behind the pin")
	}
	for _, want := range []string{
		// Both versions, so the reader can see the skew at all.
		"v0.1.43",
		"v0.1.42",
		// The cause, which is the non-obvious part: the schema ships IN
		// the binary, so this is not a project-side KCL mistake.
		"embedded",
		// Steers the reader away from the line number KCL blamed.
		"not a mistake in your KCL",
		// The literal, runnable command.
		"go install github.com/reliant-labs/forge/cmd/forge@v0.1.43",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("SkewDiagnosis missing %q\n--- message ---\n%s", want, msg)
		}
	}
}

// TestSkewDiagnosisStaysQuietWhenItCannotHelp — a diagnostic that fires on
// every dogfood build is noise, and noise is why nobody reads the real one.
// forge builds ITSELF from a working tree, so "(devel)" and `+dirty` stamps
// are the common local case and name no version to compare.
func TestSkewDiagnosisStaysQuietWhenItCannotHelp(t *testing.T) {
	cases := []struct{ name, pinned, binary string }{
		{"binary ahead of pin", "v0.1.40", "v0.1.43"},
		{"versions equal", "v0.1.43", "v0.1.43"},
		{"no pin declared", "", "v0.1.43"},
		{"dev binary", "v0.1.43", "(devel)"},
		{"dirty binary", "v0.1.43", "v0.1.42+dirty"},
		{"unstamped binary", "v0.1.43", "dev"},
		{"binary version absent", "v0.1.43", ""},
		{"pin is the first-user sentinel", "0.0.0", "v0.1.43"},
		{"pin is not semver", "garbage", "v0.1.43"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if msg := SkewDiagnosis(c.pinned, c.binary); msg != "" {
				t.Errorf("SkewDiagnosis(%q, %q) should be silent, got:\n%s",
					c.pinned, c.binary, msg)
			}
		})
	}
}

// TestPinnedForgeVersionWalksUpToTheProjectRoot — the render seam knows a
// workDir, which for `forge env render` is the project root but for other
// callers is a directory under it. The pin lives in the root's forge.yaml,
// so finding it has to walk up; otherwise the diagnosis silently never fires
// for exactly the deep-render callers that hit the bug.
func TestPinnedForgeVersionWalksUpToTheProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"),
		[]byte("name: demo\nforge_version: v0.1.43\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "deploy", "kcl", "dev")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{root, deep} {
		if got := PinnedForgeVersion(dir); got != "v0.1.43" {
			t.Errorf("PinnedForgeVersion(%s) = %q, want v0.1.43", dir, got)
		}
	}

	// No forge.yaml anywhere above → no pin, and no guess.
	if got := PinnedForgeVersion(t.TempDir()); got != "" {
		t.Errorf("PinnedForgeVersion(non-project) = %q, want \"\"", got)
	}
}

// RunPinnedCommand must paste as-is: the refused command, unchanged, run with
// the pinned forge — and quote only what a shell would otherwise reinterpret.
func TestRunPinnedCommand(t *testing.T) {
	got := RunPinnedCommand("v0.1.43", []string{"env", "build", "prod", "--target", "api", "-D", "region=us east"})
	want := "go run github.com/reliant-labs/forge/cmd/forge@v0.1.43 env build prod --target api -D 'region=us east'"
	if got != want {
		t.Errorf("RunPinnedCommand:\n got: %s\nwant: %s", got, want)
	}
	if got := RunPinnedCommand("v0.1.43", []string{"it's", ""}); got != `go run github.com/reliant-labs/forge/cmd/forge@v0.1.43 'it'\''s' ''` {
		t.Errorf("quoting: %s", got)
	}
}
