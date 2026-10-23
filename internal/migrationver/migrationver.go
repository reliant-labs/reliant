// Package migrationver allocates migration version prefixes.
//
// WHY TIMESTAMPS AND NOT max+1. A sequential allocator reads the directory
// and picks one more than the highest number in it. That is collision-free
// on ONE checkout and collision-prone across branches, because two branches
// cut from the same commit both read the same highest number and both pick
// the same next one. Neither sees the other until merge, and by then each
// has a file, a review, and often an applied migration in a shared dev
// database under that number.
//
// This is not hypothetical. In the control-plane repository ten version
// numbers were each claimed by two DIFFERENT migrations across branches
// (00091 through 00096, 00106, 00107, 00110, 00111), and 00091 was claimed
// on eight separate branches. Every one had to be renumbered by hand, and
// the numbers that reached a shared database first left it stamped with a
// version whose file no longer existed on main.
//
// A UTC timestamp removes the shared counter: two branches allocating at
// different wall-clock instants cannot choose the same value without
// coordinating, so nothing needs to read anyone else's branch. The cost is
// that versions are no longer dense (there are gaps), which nothing depends
// on — the migrator orders by version and never assumes version+1 exists.
//
// EXISTING SEQUENTIAL FILES ARE LEFT ALONE, and this is what makes adoption
// free. A 14-digit timestamp is numerically greater than any 5-digit
// sequence number (the smallest timestamp, 10000101000000, exceeds the
// largest 5-digit number by nine orders of magnitude), so every new
// migration sorts AFTER every old one with no renaming, no backfill, and no
// flag day. A project mid-adoption holds both spellings and orders
// correctly.
package migrationver

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Layout is the version prefix format: UTC year, month, day, hour, minute,
// second, as 14 digits.
//
// UTC, not local time, because the version is compared across machines. Two
// engineers in different zones allocating minutes apart must produce
// versions that order the way the work happened, and a local-time prefix
// gets that wrong by up to a day.
const Layout = "20060102150405"

// Digits is the number of digits a timestamp version has. Named so the
// linter and the allocator test the same fact rather than each writing 14.
const Digits = 14

// versionPrefixRe matches the leading numeric version of a migration
// filename ("00019_add_users.up.sql" -> "00019"). It deliberately accepts
// ANY run of digits, not just 14: a directory mid-adoption holds both
// spellings, and the allocator has to see the old ones to avoid colliding
// with them.
var versionPrefixRe = regexp.MustCompile(`^(\d+)_`)

// ParseVersion returns the numeric version prefix of a migration filename.
//
// The bool is false for a name with no numeric prefix rather than an error:
// a migrations directory legitimately holds company (.gitkeep, README) that
// is not a migration and must not fail a scan.
func ParseVersion(filename string) (uint64, bool) {
	m := versionPrefixRe.FindStringSubmatch(filename)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		// A prefix too long for uint64 — not a version forge wrote.
		return 0, false
	}
	return v, true
}

// IsTimestamp reports whether a version is in the timestamp scheme: exactly
// Digits digits AND a real calendar instant.
//
// The calendar check is what makes this meaningful. Length alone would
// accept 99999999999999, which is not a date and would sort after every
// genuine timestamp forever — a number that can only have been typed by
// hand, and exactly the mistake the linter exists to catch.
func IsTimestamp(version string) bool {
	if len(version) != Digits {
		return false
	}
	if _, err := time.Parse(Layout, version); err != nil {
		return false
	}
	return true
}

// Scan returns the set of version numbers already present in dir.
//
// A missing directory is an EMPTY set, not an error: allocating the first
// migration of a fresh project is the common case, and the caller creates
// the directory as it writes.
func Scan(dir string) (map[uint64]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[uint64]bool{}, nil
		}
		return nil, fmt.Errorf("read migrations directory %q: %w", dir, err)
	}
	taken := map[uint64]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".sql") {
			continue
		}
		if v, ok := ParseVersion(e.Name()); ok {
			taken[v] = true
		}
	}
	return taken, nil
}

// MaxVersion returns the highest version present in dir, and whether dir
// held any migration at all.
func MaxVersion(dir string) (uint64, bool, error) {
	taken, err := Scan(dir)
	if err != nil {
		return 0, false, err
	}
	var max uint64
	var any bool
	for v := range taken {
		if v > max {
			max = v
		}
		any = true
	}
	return max, any, nil
}

// Next allocates one version for a new migration in dir.
func Next(dir string) (string, error) {
	vs, err := NextN(dir, 1)
	if err != nil {
		return "", err
	}
	return vs[0], nil
}

// NextN allocates n versions in ascending order, for an importer writing a
// whole batch at once.
//
// Each is a distinct second, because the version is the identity: a batch
// that reused one instant would reintroduce, within a single command, the
// exact duplicate-version collision this package exists to remove.
//
// EVERY ALLOCATION CLEARS THE DIRECTORY'S HIGHEST VERSION, not merely avoids
// an exact match with it. Skipping collisions is not enough, and the gap was
// silent: golang-migrate records ONE current version and applies only what is
// above it, so a version allocated BELOW the highest one already merged is
// never applied on any database that has run the newer migration. No error,
// no refusal — `up` reports the schema current and the table is missing.
//
// A future-dated version on the default branch is what makes that reachable.
// control-plane's main held a hand-zeroed 20261003120000, about four hours
// ahead of the wall clock when it landed, so every migration created in that
// window got a version under the head and looked entirely ordinary.
//
// This is NOT the max+1 counter the package opens by rejecting. The floor
// only rises when the max is a FUTURE-dated timestamp; for every healthy
// directory — where the highest version is in the past — the clock decides,
// and two branches cut from the same commit still allocate independently.
// See startAfter.
func NextN(dir string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	taken, err := Scan(dir)
	if err != nil {
		return nil, err
	}
	return AllocateAfter(taken, n), nil
}

// AllocateAfter allocates n versions that avoid every version in taken,
// for a caller whose "already claimed" set is wider than one directory.
//
// `forge db migration rebase` is that caller. Re-versioning a migration has
// to clear more than the files on disk: the file being renamed may itself be
// the directory's highest version, and a fresh version equal to one already
// merged and deployed on another branch would be just as unapplyable as the
// one being fixed. So rebase seeds taken with the default branch's versions
// and the merge-base high-water mark as well as the directory's contents,
// which Scan alone cannot know about.
//
// NextN calls this too, with the directory alone. Allocating a NEW migration
// and rebasing a stale one need the same guarantee — a version above the
// head, not merely different from it — because a version below the head is
// silently never applied in both cases. NextN originally skipped only exact
// collisions, which is the defect this consolidation removes.
//
// Every allocated version is strictly GREATER than every version in taken,
// not merely different from it. Skipping collisions is not enough here: the
// whole point of a rebase is to move a file ABOVE a mark it currently sits
// below, and a version that merely differs from the mark can still be under
// it. A version dated in the future — a clock skew on the machine that
// allocated it, or a hand-typed value — would otherwise keep winning, and
// the rebase would produce a file just as unapplyable as the one it fixed.
//
// taken is not modified.
func AllocateAfter(taken map[uint64]bool, n int) []string {
	if n <= 0 {
		return nil
	}
	seed := make(map[uint64]bool, len(taken))
	var max uint64
	for v := range taken {
		seed[v] = true
		if v > max {
			max = v
		}
	}
	return allocate(seed, startAfter(max, time.Now().UTC()), n)
}

// startAfter returns the instant to begin allocating from so that every
// version produced exceeds max.
//
// Normally that is simply now: wall-clock time is ahead of every version
// allocated in the past, and a sequential version (5 digits) is nine orders
// of magnitude below any timestamp. The exception is a max that is itself a
// future-dated timestamp, where now would allocate BELOW it — then the floor
// is the second after max.
func startAfter(max uint64, now time.Time) time.Time {
	now = now.UTC().Truncate(time.Second)
	maxText := strconv.FormatUint(max, 10)
	if !IsTimestamp(maxText) {
		return now
	}
	maxTime, err := time.Parse(Layout, maxText)
	if err != nil {
		return now
	}
	if maxTime.UTC().Before(now) {
		return now
	}
	return maxTime.UTC().Add(time.Second)
}

// allocate is NextN's decision, separated from the filesystem and the clock
// so the collision behaviour is testable without either.
//
// It walks forward a second at a time past anything already taken. Advancing
// is what makes rapid successive allocations safe: `forge scaffold entity`
// twice in one second, or an import writing forty files, would otherwise
// hand out one version repeatedly. Moving FORWARD (never backward, never
// into a sub-second suffix) keeps every allocation greater than every
// version already in the directory, which is the property that lets new
// migrations coexist with the old sequential ones.
func allocate(taken map[uint64]bool, now time.Time, n int) []string {
	out := make([]string, 0, n)
	t := now.UTC().Truncate(time.Second)
	for len(out) < n {
		v := t.Format(Layout)
		parsed, _ := strconv.ParseUint(v, 10, 64)
		if !taken[parsed] {
			out = append(out, v)
			taken[parsed] = true
		}
		t = t.Add(time.Second)
	}
	return out
}
