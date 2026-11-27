package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/release"
)

var (
	filesShowLinux = release.Artifact{
		Kind: release.KindFile, Mode: release.ModeShared, Version: "dist/acme_linux_amd64.tar.gz",
		Integrity: "sha256:" + strings.Repeat("1", 64), Size: 3 << 20, Platforms: []string{"linux/amd64"},
	}
	filesShowVariant = release.Artifact{
		Kind: release.KindFile, Mode: release.ModeShared, Version: "prod",
		Integrity: "sha256:" + strings.Repeat("2", 64),
	}
)

// filesDeps is a machine ledger holding one release of a files-only CLI and a
// build-only binary, promoted once to prod.
func filesDeps(t *testing.T) releasesDeps {
	t.Helper()
	store, err := ledgerfile.Open(t.TempDir(), "proj-files")
	if err != nil {
		t.Fatal(err)
	}
	r := release.Release{Version: "v1.0.0", CreatedAt: relNow.Add(-time.Hour), Artifacts: map[string]release.Artifact{
		"acme-cli/dist/acme_linux_amd64.tar.gz": filesShowLinux,
		"acme-agent-prod":                       filesShowVariant,
		"ghcr.io/acme/api": {Kind: release.KindOCI, Mode: release.ModeShared,
			Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("a", 64)}},
	}}
	if _, err := store.CutRelease(r); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportPromotion(release.Promotion{
		ID: "p1", Env: "prod", Release: "v1.0.0", Kind: release.KindPromote,
		Resolved: map[string]string{"api": "sha256:" + strings.Repeat("a", 64)}, PromotedBy: release.Actor{Actor: "ci"},
		PromotedAt: relNow.Add(-30 * time.Minute).Truncate(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	return releasesDeps{
		open: func(context.Context, string) (releasesBackend, error) {
			return releasesBackend{Name: backendLocal, Source: releasetimeline.Local{Ledger: store}, Ledger: store.Dir(), Bundle: store.Bundle}, nil
		},
		now:     func() time.Time { return relNow },
		commits: func() releasetimeline.CommitLister { return fakeLister{} },
	}
}

func TestEnvReleasesShow_ListsTheReleasesFiles(t *testing.T) {
	out, err := runReleases(t, filesDeps(t), "show", "prod", "v1.0.0", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Files []showFile `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []showFile{
		{Name: "acme-agent-prod", Path: "acme-agent-prod", SHA256: strings.Repeat("2", 64)},
		{Name: "acme-cli/dist/acme_linux_amd64.tar.gz", Path: "dist/acme_linux_amd64.tar.gz", SHA256: strings.Repeat("1", 64),
			Size: 3 << 20, Platforms: []string{"linux/amd64"}},
	}
	if !reflect.DeepEqual(doc.Files, want) {
		t.Fatalf("files = %+v\nwant  %+v", doc.Files, want)
	}

	text, err := runReleases(t, filesDeps(t), "show", "prod", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"files", "acme-cli/dist/acme_linux_amd64.tar.gz", "3.0 MiB", "linux/amd64", strings.Repeat("1", 12)} {
		if !strings.Contains(text, want) {
			t.Errorf("show lacks %q:\n%s", want, text)
		}
	}
}

// A control plane's timeline carries no artifacts: show reads the release
// on its own, and a failure to is a warning, not an error.
func TestShowFilesOf_ReadsACloudReleaseWhole(t *testing.T) {
	rel := &release.Release{Version: "v1.0.0", Artifacts: map[string]release.Artifact{"acme-cli/dist/acme_linux_amd64.tar.gz": filesShowLinux}}
	b := releasesBackend{Release: func(_ context.Context, v string) (*release.Release, error) {
		if v != "v1.0.0" {
			t.Errorf("read release %q", v)
		}
		return rel, nil
	}}
	var doc releasesShowDoc
	showFilesOf(context.Background(), b, &doc, "v1.0.0", []release.Release{{Version: "v1.0.0"}})
	if len(doc.Files) != 1 || doc.Files[0].Path != "dist/acme_linux_amd64.tar.gz" {
		t.Fatalf("files = %+v", doc.Files)
	}

	b.Release = func(context.Context, string) (*release.Release, error) { return nil, context.DeadlineExceeded }
	doc = releasesShowDoc{}
	showFilesOf(context.Background(), b, &doc, "v1.0.0", nil)
	if len(doc.Files) != 0 || len(doc.Warnings) != 1 || !strings.Contains(doc.Warnings[0], "files of v1.0.0 not listed") {
		t.Fatalf("doc = %+v, want one warning and no files", doc)
	}
}

// Comparing two releases reports a file only when its bytes changed, and
// shows the path whole with the digest shortened.
func TestCompareArtifacts_AFileChangesOnlyWithItsBytes(t *testing.T) {
	from := release.Release{Version: "v1", Artifacts: map[string]release.Artifact{"acme-cli/dist/acme.zip": filesShowLinux}}
	to := release.Release{Version: "v2", Artifacts: map[string]release.Artifact{"acme-cli/dist/acme.zip": filesShowLinux}}
	if changed, unchanged := release.CompareArtifacts(from, to); len(changed) != 0 || unchanged != 1 {
		t.Fatalf("identical bytes reported as changed: %+v", changed)
	}
	moved := filesShowLinux
	moved.Integrity = "sha256:" + strings.Repeat("9", 64)
	to.Artifacts["acme-cli/dist/acme.zip"] = moved
	changed, _ := release.CompareArtifacts(from, to)
	if len(changed) != 1 {
		t.Fatalf("changed = %+v", changed)
	}
	if got := shortArtifactID(changed[0].To); got != "dist/acme_linux_amd64.tar.gz sha256:999999999999" {
		t.Errorf("shortArtifactID = %q", got)
	}
}

// Size rides the hosted wire and comes back; a snapshot document, which a
// server decodes strictly, carries none.
func TestHostedWire_CarriesAFileSize(t *testing.T) {
	r := release.Release{Version: "v1.0.0", Artifacts: map[string]release.Artifact{"acme-cli/dist/acme_linux_amd64.tar.gz": filesShowLinux}}
	rows := releaseToWire(r)
	if len(rows) != 1 || rows[0].Size.Int64() != filesShowLinux.Size {
		t.Fatalf("wire rows = %+v", rows)
	}
	raw, err := json.Marshal(wireRelease{Version: r.Version, Artifacts: rows})
	if err != nil {
		t.Fatal(err)
	}
	var w wireRelease
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	back, err := releaseFromWire(w)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Artifacts["acme-cli/dist/acme_linux_amd64.tar.gz"].Size; got != filesShowLinux.Size {
		t.Errorf("size read back = %d, want %d", got, filesShowLinux.Size)
	}
	// protojson sends an int64 as a string.
	if err := json.Unmarshal([]byte(`{"name":"f","kind":"file","mode":"shared","version":"f","size":"42"}`), &w.Artifacts[0]); err != nil || w.Artifacts[0].Size.Int64() != 42 {
		t.Errorf("string-encoded size: %v / %d", err, w.Artifacts[0].Size.Int64())
	}

	stripped := artifactsWithoutSize(r.Artifacts)
	if stripped["acme-cli/dist/acme_linux_amd64.tar.gz"].Size != 0 || r.Artifacts["acme-cli/dist/acme_linux_amd64.tar.gz"].Size == 0 {
		t.Error("artifactsWithoutSize must clear a copy, never the release")
	}
}
