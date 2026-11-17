package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

type snapshotCaller struct {
	capable bool
	calls   []string
	cut     map[string]any
}

func (f *snapshotCaller) Call(_ context.Context, proc string, in, out any) error {
	f.calls = append(f.calls, proc)
	var response any
	switch proc {
	case procDeployCapabilities:
		response = map[string]any{"snapshotSchemaVersions": []string{release.SnapshotSchema}, "immutableSnapshotSeals": f.capable, "frozenPromotionBundles": f.capable}
	case procCutRelease:
		data, _ := json.Marshal(in)
		_ = json.Unmarshal(data, &f.cut)
		response = map[string]any{"created": true}
	default:
		return fmt.Errorf("unexpected procedure %s", proc)
	}
	data, _ := json.Marshal(response)
	return json.Unmarshal(data, out)
}
func snapshotCatalogRelease(t *testing.T) release.Release {
	t.Helper()
	d := "sha256:" + strings.Repeat("a", 64)
	a := release.SnapshotArtifact{Kind: release.KindOCI, Location: "registry.example/reliant", Content: d, Platforms: []string{"linux/amd64"}, Origin: release.ArtifactOrigin{Release: "v1", Source: &release.Source{Repo: "example/reliant", Commit: strings.Repeat("b", 40)}}}
	id, err := release.ArtifactIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	return release.Release{Version: "v1", Project: "p", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Catalog: map[release.ArtifactID]release.SnapshotArtifact{id: a}, SnapshotIntents: map[string]string{"prod": d}}
}
func TestSnapshotCutRequiresCapabilityAndNoLegacyFlattening(t *testing.T) {
	r := snapshotCatalogRelease(t)
	for _, capable := range []bool{false, true} {
		fake := &snapshotCaller{capable: capable}
		store := &hostedStore{client: fake}
		_, err := store.Cut(context.Background(), r)
		if !capable {
			if err == nil || fake.cut != nil {
				t.Fatal("legacy server accepted snapshot write")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if fake.cut["snapshotRelease"] == nil || fake.cut["artifacts"] != nil || fake.cut["provenance"] != nil {
			t.Fatalf("lossy or ambiguous wire %v", fake.cut)
		}
	}
}
func TestSnapshotReleaseRoundTripStrict(t *testing.T) {
	r := snapshotCatalogRelease(t)
	raw, _ := json.Marshal(r)
	got, err := releaseFromWire(wireRelease{Version: r.Version, SnapshotRelease: raw})
	if err != nil {
		t.Fatal(err)
	}
	if !r.SameContent(got) {
		t.Fatal("snapshot identity lost on wire")
	}
	if _, err := releaseFromWire(wireRelease{Version: "wrong", SnapshotRelease: raw}); err == nil {
		t.Fatal("accepted mismatching envelope")
	}
	raw = append(raw[:len(raw)-1], []byte(`,"unrecognized":true}`)...)
	if _, err := snapshotReleaseFromJSON(raw, r.Version); err == nil {
		t.Fatal("silently discarded unknown snapshot fields")
	}
}
