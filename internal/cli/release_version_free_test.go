package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The 2026-10-25 collision: v1.7.57 was cut, never promoted, and a second cut
// of v1.7.57 from another commit went ahead, because the check before it read
// the promotion timeline only. A version is taken once it is CUT.
//
// Mutation that fails it: return nil from checkReleaseVersionFree for a
// release that has no promotion.
func TestReleaseVersionFree_RefusesAVersionAlreadyCutFromAnotherCommit(t *testing.T) {
	dir := t.TempDir()
	ledger := testLedger(t, dir)
	const cutFrom = "d4f71ded0a08d4f71ded0a08d4f71ded0a08d4f7"
	cut := release.Release{
		Version:   "v1.7.57",
		CreatedAt: time.Date(2026, 10, 25, 4, 1, 57, 0, time.UTC),
		Artifacts: map[string]release.Artifact{
			"us-central1-docker.pkg.dev/acme/prod/control-plane": {Kind: release.KindOCI, Mode: release.ModeShared,
				Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("a", 64)}},
		},
	}
	cut.SetProvenance(release.Provenance{Commit: cutFrom})
	if _, err := ledger.Releases.Cut(context.Background(), cut); err != nil {
		t.Fatalf("cut: %v", err)
	}

	err := checkReleaseVersionFree(context.Background(), ledger, "prod", "v1.7.57", release.Provenance{Commit: "b7059e22368bb7059e22368bb7059e22368bb705"})
	if !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("a second cut of v1.7.57 from another commit must be refused as a release conflict, got %v", err)
	}
	for _, want := range []string{"v1.7.57 is already cut", "commit d4f71ded0a08", "2026-10-25T04:01:57Z", "commit b7059e22368b", "forge env deploy prod v1.7.57"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%s", want, err)
		}
	}

	// The same clean commit is a retry, and stays possible.
	if err := checkReleaseVersionFree(context.Background(), ledger, "prod", "v1.7.57", release.Provenance{Commit: cutFrom}); err != nil {
		t.Errorf("a retry of the cut from the same clean commit must not be refused: %v", err)
	}
	// The same commit with uncommitted changes is NOT that source.
	if err := checkReleaseVersionFree(context.Background(), ledger, "prod", "v1.7.57", release.Provenance{Commit: cutFrom, Dirty: true}); !errors.Is(err, release.ErrReleaseConflict) {
		t.Errorf("a dirty tree is not the source the release was cut from; got %v", err)
	}
	// A version nobody cut is free.
	if err := checkReleaseVersionFree(context.Background(), ledger, "prod", "v1.7.58", release.Provenance{Commit: cutFrom}); err != nil {
		t.Errorf("an uncut version must be free: %v", err)
	}
}
