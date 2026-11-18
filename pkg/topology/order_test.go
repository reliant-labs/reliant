package topology

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad fixture time %q: %v", s, err)
	}
	return ts
}

// TestSortReleasesNewestFirst pins CUT TIME as the order, across version
// schemes. Semver cannot parse a date-named auto cut, so a semver-first sort
// ranked every v1.x.y above every date-named release and called a forward move
// from v1.7.40 to 20261014.042859-cd678a0d6531 "67 releases OLDER". A lexical
// sort additionally puts v1.5.9 after v1.5.15.
func TestSortReleasesNewestFirst(t *testing.T) {
	t.Run("date-named cuts after semver cuts are newer", func(t *testing.T) {
		releases := []release.Release{
			{Version: "v1.7.38", CreatedAt: mustTime(t, "2026-10-09T00:00:00Z")},
			{Version: "20261014.042859-cd678a0d6531", CreatedAt: mustTime(t, "2026-10-14T04:28:59Z")},
			{Version: "v1.7.40", CreatedAt: mustTime(t, "2026-10-12T00:00:00Z")},
			{Version: "20261013.002646-aaaaaaaaaaaa", CreatedAt: mustTime(t, "2026-10-13T00:26:46Z")},
		}
		SortReleasesNewestFirst(releases)
		want := []string{"20261014.042859-cd678a0d6531", "20261013.002646-aaaaaaaaaaaa", "v1.7.40", "v1.7.38"}
		for i, w := range want {
			if releases[i].Version != w {
				t.Fatalf("order[%d] = %q, want %q (full: %v)", i, releases[i].Version, w, versionsOf(releases))
			}
		}
	})

	t.Run("semver labels cut at the same instant fall back to semver, not lexical", func(t *testing.T) {
		at := mustTime(t, "2026-08-01T00:00:00Z")
		releases := []release.Release{
			{Version: "v1.5.9", CreatedAt: at}, {Version: "v1.5.15", CreatedAt: at}, {Version: "v1.3.0", CreatedAt: at},
		}
		SortReleasesNewestFirst(releases)
		if got := versionsOf(releases); got[0] != "v1.5.15" || got[1] != "v1.5.9" || got[2] != "v1.3.0" {
			t.Fatalf("order = %v, want [v1.5.15 v1.5.9 v1.3.0]", got)
		}
	})

	t.Run("an unknown cut time sorts oldest and the order is a stable total order", func(t *testing.T) {
		releases := []release.Release{
			{Version: "v1.0.0"},
			{Version: "v1.1.0", CreatedAt: mustTime(t, "2026-01-01T00:00:00Z")},
			{Version: "20260301.000000-abc", CreatedAt: mustTime(t, "2026-03-01T00:00:00Z")},
		}
		SortReleasesNewestFirst(releases)
		if got := versionsOf(releases); got[0] != "20260301.000000-abc" || got[1] != "v1.1.0" || got[2] != "v1.0.0" {
			t.Fatalf("order = %v", got)
		}
	})
}

func versionsOf(rs []release.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

// TestImageState_RejectsUnknown: an unrecognized state must NOT decode
// to the zero value. Here the zero value is not_verified rather than match, so
// even a silent default would be safe — but a newer forge with a seventh state
// must be visible as version skew rather than silently flattened.
func TestImageState_RejectsUnknown(t *testing.T) {
	var s ImageState
	if err := json.Unmarshal([]byte(`"probably_fine"`), &s); err == nil {
		t.Fatal("an unknown state must be an error, not a default")
	}
	for _, name := range []string{"not_verified", "match", "drift", "missing", "untagged", "unreachable"} {
		var got ImageState
		if err := json.Unmarshal([]byte(`"`+name+`"`), &got); err != nil {
			t.Fatalf("%q must decode: %v", name, err)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal %q: %v", name, err)
		}
		if string(encoded) != `"`+name+`"` {
			t.Errorf("round trip of %q produced %s", name, encoded)
		}
	}
	// The zero value is the unknown state, so a never-populated struct
	// reads as "not checked" rather than as a clean bill of health.
	var zero ImageState
	if zero != NotVerified {
		t.Error("the zero value must be not_verified — a zero-value struct must never read as healthy")
	}
}
