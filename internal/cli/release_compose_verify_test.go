package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestSnapshotVerifyCatalogDoesNotVacuouslyPassOrCollapseRepository(t *testing.T) {
	r := snapshotCatalogRelease(t)
	var a release.SnapshotArtifact
	for _, value := range r.Catalog {
		a = value
	}
	a.Content = "sha256:" + strings.Repeat("b", 64)
	id, err := release.ArtifactIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	r.Catalog[id] = a
	// No HTTP is needed to prove work inventory; a cancelled transport must
	// yield two unverifiable/unreachable records, never a vacuous empty PASS.
	got := verifyReleaseArtifacts(context.Background(), snapshotUnreachableFetcher{}, r, 2)
	if len(got) != 2 {
		t.Fatalf("catalog collapsed or omitted: %d", len(got))
	}
	if got[0].Name == got[1].Name {
		t.Fatal("repository reused as content identity")
	}
	for _, result := range got {
		if result.Status == verifyVerified {
			t.Fatal("outage claimed verification")
		}
	}
}

type snapshotUnreachableFetcher struct{}

func (snapshotUnreachableFetcher) Fetch(context.Context, fetchRequest) (fetchResponse, error) {
	return fetchResponse{}, context.DeadlineExceeded
}
