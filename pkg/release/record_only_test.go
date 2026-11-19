package release

import (
	"encoding/json"
	"errors"
	"testing"
)

// A release forge BUILT that names nothing is a build that lost its outputs,
// and stays refused. Only an explicitly record-only release may be
// version-only.
func TestRelease_VersionOnlyNeedsRecordOnly(t *testing.T) {
	built := Release{Version: "v1", Git: Git{Commit: "abc123"}}
	if err := built.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a forge-built release with no artifacts must be refused, got %v", err)
	}

	recorded := Release{Version: "v1", Git: Git{Commit: "abc123"}, RecordOnly: true}
	if err := recorded.Validate(); err != nil {
		t.Fatalf("a record-only release with no artifacts must validate: %v", err)
	}
	if !recorded.VersionOnly() {
		t.Fatal("a release with no artifacts is version-only")
	}

	withDigest := Release{Version: "v1", RecordOnly: true, Artifacts: map[string]Artifact{"api": image(digest("a"))}}
	if err := withDigest.Validate(); err != nil {
		t.Fatalf("a record-only release with a CI-built digest must validate: %v", err)
	}
	if withDigest.VersionOnly() {
		t.Fatal("a release naming an image is not version-only")
	}
}

// A record-only release never carries forge composition: there is no bundle
// forge rendered for it.
func TestRelease_RecordOnlyRefusesComposition(t *testing.T) {
	r := Release{
		Version:         "v1",
		RecordOnly:      true,
		SnapshotIntents: map[string]string{"prod": digest("b")},
	}
	if err := r.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("record-only with snapshot intents must be refused, got %v", err)
	}
}

// The commit is a version-only release's content: the same version at the
// same commit is a retry, at another commit a conflict. Artifact releases keep
// their digest identity, with the record-only flag outside it.
func TestRelease_RecordOnlyIdentity(t *testing.T) {
	a := Release{Version: "v1", Git: Git{Commit: "abc"}, RecordOnly: true}
	if err := CheckRecut(a, Release{Version: "v1", Git: Git{Commit: "abc"}, RecordOnly: true, Run: Run{ID: "r2"}}); err != nil {
		t.Fatalf("re-recording at the same commit is a retry: %v", err)
	}
	if err := CheckRecut(a, Release{Version: "v1", Git: Git{Commit: "def"}, RecordOnly: true}); !errors.Is(err, ErrReleaseConflict) {
		t.Fatalf("re-recording at another commit must conflict, got %v", err)
	}
	built := Release{Version: "v1", Git: Git{Commit: "abc"}, Artifacts: map[string]Artifact{"api": image(digest("a"))}}
	if err := CheckRecut(built, a); !errors.Is(err, ErrReleaseConflict) {
		t.Fatalf("a version-only record of a built version must conflict, got %v", err)
	}
	recorded := built
	recorded.RecordOnly = true
	if err := CheckRecut(built, recorded); err != nil {
		t.Fatalf("recording the digests forge built is the same content: %v", err)
	}
}

// The flag rides the JSON both ways, is omitted when false (so every ledger
// written before it reads and writes unchanged), and reaches the promotion.
func TestRelease_RecordOnlyRoundTripsAndReachesThePromotion(t *testing.T) {
	r := Release{Version: "v1", Git: Git{Commit: "abc"}, RecordOnly: true}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Release
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.RecordOnly {
		t.Fatalf("record_only lost in %s", raw)
	}
	plain, _ := json.Marshal(Release{Version: "v1", Artifacts: map[string]Artifact{"api": image(digest("a"))}})
	if json.Valid(plain) && containsKey(plain, "record_only") {
		t.Fatalf("record_only must be omitted when false: %s", plain)
	}

	p := NewPromotion("prod", r, KindPromote)
	if !p.RecordOnly {
		t.Fatal("a promotion of a record-only release is record-only")
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("a version-only promotion must validate: %v", err)
	}
}

func containsKey(raw []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
