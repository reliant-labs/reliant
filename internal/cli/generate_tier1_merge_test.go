package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// TestDetectGitMergeState_DetectsEachMarker covers each marker shape
// the Tier-1 stomp guard's mid-merge hint relies on. The fakes mirror
// what `git merge`, `git cherry-pick`, and `git rebase` would deposit
// under .git/ at the host repo's working root.
func TestDetectGitMergeState_DetectsEachMarker(t *testing.T) {
	cases := []struct {
		name   string
		seed   func(t *testing.T, gitDir string)
		expect string
	}{
		{
			name: "no markers",
			seed: func(t *testing.T, gitDir string) {
				// Intentionally empty .git/ — no merge state.
			},
			expect: "",
		},
		{
			name: "MERGE_HEAD present",
			seed: func(t *testing.T, gitDir string) {
				writeForTest(t, filepath.Join(gitDir, "MERGE_HEAD"), "deadbeef\n")
			},
			expect: "merge",
		},
		{
			name: "CHERRY_PICK_HEAD present",
			seed: func(t *testing.T, gitDir string) {
				writeForTest(t, filepath.Join(gitDir, "CHERRY_PICK_HEAD"), "deadbeef\n")
			},
			expect: "cherry-pick",
		},
		{
			name: "rebase-merge/ directory present",
			seed: func(t *testing.T, gitDir string) {
				if err := os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			expect: "rebase",
		},
		{
			name: "rebase-apply/ directory present",
			seed: func(t *testing.T, gitDir string) {
				if err := os.MkdirAll(filepath.Join(gitDir, "rebase-apply"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			expect: "rebase",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitDir := filepath.Join(dir, ".git")
			if err := os.MkdirAll(gitDir, 0o755); err != nil {
				t.Fatal(err)
			}
			tc.seed(t, gitDir)

			got := detectGitMergeState(dir)
			if got != tc.expect {
				t.Errorf("detectGitMergeState() = %q, want %q", got, tc.expect)
			}
		})
	}
}

// TestStepCheckTier1Drift_MidMergeReturnsTypedError exercises the
// happy-path: project is mid-merge AND a Tier-1 file has drifted →
// the step returns the typed errMidMergeTier1Drift carrying both the
// detected git state and a friendlier message. The default cobra path
// still prints the message; tools that want to recognize this state
// (e.g. an LLM agent harness) can type-assert.
func TestStepCheckTier1Drift_MidMergeReturnsTypedError(t *testing.T) {
	dir := t.TempDir()

	// Seed a certified Tier-1 file whose body no longer matches its
	// embedded forge:hash marker (the merge brought in different bytes)
	// so the stomp guard fires.
	rel := "handlers_gen.go"
	original := []byte("// generated v1\npackage handlers\n")
	modified := []byte("// upstream-merged v2\npackage handlers\n")
	stamped, ok := checksums.StampWithValue(rel, modified, checksums.BodyHash(original))
	if !ok {
		t.Fatal("handlers_gen.go should be stampable")
	}
	writeForTest(t, filepath.Join(dir, rel), string(stamped))

	cs := &checksums.FileChecksums{}

	// Plant a MERGE_HEAD so detectGitMergeState reports "merge".
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeForTest(t, filepath.Join(gitDir, "MERGE_HEAD"), "deadbeef\n")

	ctx := &pipelineContext{
		ProjectDir: dir,
		AbsPath:    dir,
		Checksums:  cs,
	}

	err := checkTier1DriftAndAdjudicate(t, ctx)
	if err == nil {
		t.Fatal("expected stepCheckTier1Drift to return an error (Tier-1 drift detected)")
	}

	var typed errMidMergeTier1Drift
	if !errors.As(err, &typed) {
		t.Fatalf("expected errMidMergeTier1Drift, got %T: %v", err, err)
	}
	if typed.GitMergeState() != "merge" {
		t.Errorf("GitMergeState() = %q, want %q", typed.GitMergeState(), "merge")
	}
	msg := err.Error()
	for _, want := range []string{
		"mid-merge",
		"forge generate --force",
		rel,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to mention %q, got:\n%s", want, msg)
		}
	}
}
