package obsquery

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strconv"
	"time"
)

// Incident grouping: which issues are one incident.
//
// One dead dependency breaks every caller, and every caller logs from its own
// call site, so one root cause shows up as dozens of issues (a database
// outage: one per service, worker and reconciler sweep). Issues are never
// merged for that; they keep their own fingerprints, counts and triage state.
// Instead each is marked with an incident key that issues of one incident
// share, and a list can collapse on it.
//
// Two issues are of one incident when they share a CAUSE (ServerCause:
// the SQLSTATE the error carries, the target a dial was refused at, else the
// normalized error class), in any service of the environment, and were first
// seen within a window of one another. Clustering is anchored, not chained:
// issues of a cause are ordered by first_seen, an issue within the window of its
// cluster's FIRST issue joins it, and the next one starts a new cluster. Every
// member of an incident is therefore within the window of every other, a slow
// drip of one cause over a day never becomes one unbounded incident, and the
// result does not depend on how the list is paged.
//
// "First seen" is the issue's first event in the queried range, on every backend,
// so the local and hosted answers agree.

// DefaultIncidentWindow is how close in first-seen time issues of one cause must
// be to be one incident.
const DefaultIncidentWindow = 10 * time.Minute

// MaxIncidentWindow bounds a requested window.
const MaxIncidentWindow = time.Hour

// MaxIncidentCandidates bounds the issues one incident search reads. A range
// with more distinct failing issues than this groups the newest of them.
const MaxIncidentCandidates = 2000

// incidentKeyPrefix versions the key's derivation, not the fingerprint's.
const incidentKeyPrefix = "inc1-"

// IncidentCandidate is one issue's incident inputs: the issue's identity, the
// cause of its newest event and its first event in the range.
type IncidentCandidate struct {
	// ID is the caller's handle for the issue; it comes back in IncidentGroup.
	ID        string
	Cause     string
	FirstSeen time.Time
}

// IncidentGroup is one incident: two or more issues of one cause first seen
// within the window.
type IncidentGroup struct {
	Key   string
	Cause string
	// First is the first-seen time of the incident's earliest issue.
	First time.Time
	// IDs are the members, earliest first seen first.
	IDs []string
}

// IncidentKey is the key issues of one incident share: stable for a given cause
// and anchor, and short enough to read aloud.
func IncidentKey(cause string, anchor time.Time) string {
	sum := sha256.Sum256([]byte(incidentKeyPrefix + cause + "\x1f" + strconv.FormatInt(anchor.Unix(), 10)))
	return incidentKeyPrefix + hex.EncodeToString(sum[:6])
}

// GroupIncidents clusters candidates into incidents. An issue with no cause, or
// alone in its window, belongs to no incident and is not returned. The result is
// ordered by the incident's first-seen time, then its key.
func GroupIncidents(cands []IncidentCandidate, window time.Duration) []IncidentGroup {
	if window <= 0 {
		window = DefaultIncidentWindow
	}
	byCause := map[string][]IncidentCandidate{}
	for _, c := range cands {
		if c.Cause != "" && !c.FirstSeen.IsZero() {
			byCause[c.Cause] = append(byCause[c.Cause], c)
		}
	}
	var out []IncidentGroup
	for cause, group := range byCause {
		sort.SliceStable(group, func(i, j int) bool {
			if !group[i].FirstSeen.Equal(group[j].FirstSeen) {
				return group[i].FirstSeen.Before(group[j].FirstSeen)
			}
			return group[i].ID < group[j].ID
		})
		for start := 0; start < len(group); {
			end := start + 1
			for end < len(group) && group[end].FirstSeen.Sub(group[start].FirstSeen) <= window {
				end++
			}
			if end-start >= 2 {
				g := IncidentGroup{Key: IncidentKey(cause, group[start].FirstSeen), Cause: cause, First: group[start].FirstSeen}
				for _, c := range group[start:end] {
					g.IDs = append(g.IDs, c.ID)
				}
				out = append(out, g)
			}
			start = end
		}
	}
	slices.SortFunc(out, func(a, b IncidentGroup) int {
		if c := a.First.Compare(b.First); c != 0 {
			return c
		}
		switch {
		case a.Key < b.Key:
			return -1
		case a.Key > b.Key:
			return 1
		}
		return 0
	})
	return out
}

// IncidentOf indexes groups by member id.
func IncidentOf(groups []IncidentGroup) map[string]IncidentGroup {
	m := map[string]IncidentGroup{}
	for _, g := range groups {
		for _, id := range g.IDs {
			m[id] = g
		}
	}
	return m
}

// RelatedCount is how many other issues share g with one of its members.
func (g IncidentGroup) RelatedCount() int { return len(g.IDs) - 1 }
