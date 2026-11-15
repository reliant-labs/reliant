package topology

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/pkg/release"
)

// SortReleasesNewestFirst orders releases newest first, by WHEN THEY WERE CUT.
//
// CUT TIME LEADS, and the version label never decides recency between two
// releases that were cut at different moments. A project's labels are not one
// scheme: control-plane's ledger holds semver names (v1.7.38, v1.7.40) AND
// date-named auto cuts (20261014.042859-cd678a0d6531). Semver cannot parse the
// date names, so a semver-first comparator ranked EVERY v1.x.y above EVERY
// date-named cut — and a forward promote from v1.7.40 to a cut made days later
// was reported as "BEHIND, 67 releases OLDER". Time is the one key every
// scheme shares.
//
// Where the cut times are equal or unknown (a zero created_at sorts as oldest),
// semver orders two parseable labels, a parseable label precedes an
// unparseable one, and the label itself is the final tie-break so the order is
// stable across runs rather than dependent on directory iteration. Each of
// those is a total order on its own, so the composite is one too — a
// comparator that mixed "semver when both parse, time otherwise" would be
// intransitive and sort differently from run to run.
//
// Callers that need the DIRECTION of a move between two releases consult
// commit ancestry first; this order is the fallback for releases whose
// commits are not comparable, and what "latest" and "releases behind" count
// by.
func SortReleasesNewestFirst(releases []release.Release) {
	sort.SliceStable(releases, func(i, j int) bool {
		ti, tj := releases[i].CreatedAt, releases[j].CreatedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		vi, vj := semverKey(releases[i].Version), semverKey(releases[j].Version)
		if vi != "" && vj != "" {
			if c := semver.Compare(vi, vj); c != 0 {
				return c > 0
			}
		}
		if (vi != "") != (vj != "") {
			return vi != ""
		}
		return releases[i].Version > releases[j].Version
	})
}

// semverKey normalizes a version label for semver comparison, or returns ""
// when it is not one: a missing leading "v" is added and build metadata is
// dropped (SemVer gives it no precedence).
func semverKey(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// PromotionLag measures how far behind the newest release `bound` is.
//
// releases must be newest-first ([SortReleasesNewestFirst]), so the index of
// the bound release IS the number of releases cut after it. Counting by INDEX
// rather than by parsing version numbers is what makes this correct for the
// version schemes semver cannot order: the ordering was already decided once,
// and re-deriving it here is how two answers about the same project start to
// disagree. Nil when releases is empty or does not contain bound.
func PromotionLag(releases []release.Release, bound release.Release) *Lag {
	if len(releases) == 0 {
		return nil
	}
	idx := -1
	for i, rel := range releases {
		if rel.Version == bound.Version {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	return lagAt(releases[0], bound, idx)
}

// lagAt is the lag of bound, idx releases behind latest.
func lagAt(latest, bound release.Release, idx int) *Lag {
	lag := &Lag{
		LatestRelease:  latest.Version,
		Current:        idx == 0,
		ReleasesBehind: idx,
	}
	// The time gap is best-effort. An unparseable timestamp leaves both
	// the seconds and the rendered string empty, so a consumer sees "not
	// known" rather than "zero" — a release with a broken created_at is
	// not a release that shipped today.
	newest, cut := latest.CreatedAt, bound.CreatedAt
	if !newest.IsZero() && !cut.IsZero() && newest.After(cut) {
		d := newest.Sub(cut)
		lag.BehindSeconds = int64(d.Seconds())
		lag.Behind = HumanizeLag(d)
	}
	return lag
}

// HumanizeLag renders a duration at the granularity a release cadence is
// actually read at: days and hours, not the 1713h0m0s time.Duration prints.
// Empty for a non-positive duration.
func HumanizeLag(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// formatTime is RFC3339 UTC, or "" for the zero time — an unknown instant is
// not the epoch.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
