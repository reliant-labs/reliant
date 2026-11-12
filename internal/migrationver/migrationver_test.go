package migrationver

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestAllocateAdvancesPastACollision is the property the whole package
// exists for, at the one scale a single process controls: two allocations in
// the SAME second must not produce the same version.
//
// A sequential allocator got this right by construction (every file written
// raises the max) and got the cross-branch case wrong. A timestamp allocator
// inverts that, so the same-second case is the one that needs pinning.
func TestAllocateAdvancesPastACollision(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	got := allocate(map[uint64]bool{}, now, 3)
	want := []string{"20260930120000", "20260930120001", "20260930120002"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allocate[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestAllocateSkipsVersionsAlreadyOnDisk covers the re-run: a directory that
// already holds the version this second would produce (a previous command in
// the same second) must not get a duplicate.
func TestAllocateSkipsVersionsAlreadyOnDisk(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	taken := map[uint64]bool{20260930120000: true, 20260930120001: true}
	got := allocate(taken, now, 1)
	if got[0] != "20260930120002" {
		t.Errorf("allocate = %q, want 20260930120002 (first free second)", got[0])
	}
}

// TestAllocatedVersionExceedsEverySequentialVersion is the claim that makes
// adoption free: existing NNNNN files are never renamed, so a new timestamp
// must sort after them. If this ever fails, every project mid-adoption
// silently applies its migrations in the wrong order.
func TestAllocatedVersionExceedsEverySequentialVersion(t *testing.T) {
	// The largest 5-digit sequence number, and a generous margin beyond it.
	for _, seq := range []uint64{99999, 999999} {
		got := allocate(map[uint64]bool{}, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), 1)
		v, err := strconv.ParseUint(got[0], 10, 64)
		if err != nil {
			t.Fatalf("allocated version %q is not numeric: %v", got[0], err)
		}
		if v <= seq {
			t.Errorf("allocated %d does not exceed sequential version %d — new migrations would sort BEFORE existing ones", v, seq)
		}
	}
}

// TestIsTimestampRejectsNonDates pins that the check is a calendar check and
// not a length check. 99999999999999 is 14 digits and sorts after every real
// timestamp forever; accepting it would let the one mistake the linter is
// meant to catch through.
func TestIsTimestampRejectsNonDates(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"20260930120000", true},
		{"20000101000000", true},
		{"99999999999999", false}, // 14 digits, not a date
		{"20261330120000", false}, // month 13
		{"20260932120000", false}, // day 32
		{"00001", false},
		{"2026093012000", false},   // 13 digits
		{"202609301200000", false}, // 15 digits
	}
	for _, tc := range cases {
		if got := IsTimestamp(tc.version); got != tc.want {
			t.Errorf("IsTimestamp(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name   string
		want   uint64
		wantOK bool
	}{
		{"00019_add_users.up.sql", 19, true},
		{"20260930120000_add_users.up.sql", 20260930120000, true},
		{"README.md", 0, false},
		{".gitkeep", 0, false},
		{"no_leading_digits.up.sql", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseVersion(tc.name)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("ParseVersion(%q) = (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

// TestNextSkipsVersionsInARealDirectory exercises the filesystem path,
// including the mid-adoption directory that holds both spellings.
func TestNextSkipsVersionsInARealDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"00001_init.up.sql", "00002_users.up.sql", "README.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Next(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !IsTimestamp(got) {
		t.Errorf("Next = %q, want a timestamp version", got)
	}
	v, _ := strconv.ParseUint(got, 10, 64)
	if v <= 2 {
		t.Errorf("Next = %d, want greater than the existing sequential versions", v)
	}
}

// TestNextOnAMissingDirectory is the fresh project: the first migration is
// allocated before the directory exists.
func TestNextOnAMissingDirectory(t *testing.T) {
	got, err := Next(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("Next on a missing dir should allocate, got error: %v", err)
	}
	if !IsTimestamp(got) {
		t.Errorf("Next = %q, want a timestamp version", got)
	}
}

// TestNextStaysAboveAFutureDatedMaxVersion is the silent-skip hazard.
//
// golang-migrate tracks ONE current version and applies only what is above
// it. So a newly allocated version that lands BELOW a version already in the
// directory is not a style problem — on every database that has already run
// the newer migration, the new one is never applied, and nothing reports it.
// Tests pass, `up` says the schema is current, and the table is missing.
//
// A future-dated max is what makes that reachable, and it is not
// hypothetical: control-plane's main held 20261003120000, hand-zeroed to a
// time about four hours ahead of the wall clock when it landed. Every
// migration allocated in that window got a version under the head.
//
// The allocator only ever skipped EXACT collisions, so `now` was returned
// unchanged — a version four hours below the head, which looks completely
// ordinary.
func TestNextStaysAboveAFutureDatedMaxVersion(t *testing.T) {
	dir := t.TempDir()
	// A year in the future: large enough that no plausible clock skew on the
	// machine running this test can reach it.
	const futureDated = "21000101000000_add_accounts.up.sql"
	if err := os.WriteFile(filepath.Join(dir, futureDated), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Next(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !IsTimestamp(got) {
		t.Errorf("Next = %q, which is not a 14-digit UTC calendar timestamp — the version lint would flag it", got)
	}
	v, err := strconv.ParseUint(got, 10, 64)
	if err != nil {
		t.Fatalf("allocated version %q is not numeric: %v", got, err)
	}
	if v <= 21000101000000 {
		t.Errorf("Next = %d, which is at or below the directory's max 21000101000000 — "+
			"golang-migrate applies only versions ABOVE the recorded one, so this migration "+
			"would be silently skipped on every database that already ran the newer file", v)
	}
}

// TestNextNKeepsAWholeBatchAboveAFutureDatedMax is the same guarantee for the
// importer, which allocates every version in one call. A batch that cleared
// the head with its first version and not its last would apply partially,
// which is harder to diagnose than not applying at all.
func TestNextNKeepsAWholeBatchAboveAFutureDatedMax(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "21000101000000_head.up.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := NextN(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("NextN = %v, want 3 versions", got)
	}
	var previous uint64
	for i, version := range got {
		if !IsTimestamp(version) {
			t.Errorf("NextN[%d] = %q, not a UTC calendar timestamp", i, version)
		}
		v, err := strconv.ParseUint(version, 10, 64)
		if err != nil {
			t.Fatalf("NextN[%d] = %q is not numeric: %v", i, version, err)
		}
		if v <= 21000101000000 {
			t.Errorf("NextN[%d] = %d, at or below the directory max 21000101000000 — silently unapplyable", i, v)
		}
		if v <= previous {
			t.Errorf("NextN[%d] = %d does not advance past %d; a batch must stay in order", i, v, previous)
		}
		previous = v
	}
}

// TestNextIsStillWallClockWithoutAFutureDatedMax is the negative, and it is
// the one that keeps the fix honest. Raising the floor to clear the head must
// not turn every allocation into "max+1": that is the shared counter the
// timestamp scheme exists to remove, and reintroducing it would make two
// branches cut from the same commit pick the same version again.
//
// With a max in the PAST — which is every healthy directory — the allocated
// version must come from the clock, not from the max.
func TestNextIsStillWallClockWithoutAFutureDatedMax(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "20000101000000_old.up.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Next(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Allocated from the clock means "within a minute of now", not "one
	// second after the year-2000 max".
	now := time.Now().UTC()
	allocated, err := time.Parse(Layout, got)
	if err != nil {
		t.Fatalf("Next = %q, not parseable as %s: %v", got, Layout, err)
	}
	if skew := now.Sub(allocated); skew < -time.Minute || skew > time.Minute {
		t.Errorf("Next = %q, which is %v from now — a past max must leave the clock in charge, "+
			"or the allocator has become the max+1 counter that collides across branches", got, skew)
	}
}

func TestMaxVersion(t *testing.T) {
	dir := t.TempDir()
	if _, any, err := MaxVersion(dir); err != nil || any {
		t.Errorf("MaxVersion(empty) = (_, %v, %v), want (_, false, nil)", any, err)
	}
	for _, name := range []string{"00001_init.up.sql", "00042_late.up.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	max, any, err := MaxVersion(dir)
	if err != nil || !any || max != 42 {
		t.Errorf("MaxVersion = (%d, %v, %v), want (42, true, nil)", max, any, err)
	}
}
