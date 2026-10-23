package releasetimeline

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Direction says which way a promotion moved the environment.
type Direction string

const (
	// DirInitial means the environment had never been bound.
	DirInitial Direction = "initial"
	// DirAhead means the promoted release is newer than the one it replaced.
	DirAhead Direction = "ahead"
	// DirBehind means the promoted release is OLDER than the one it replaced:
	// a rollback in everything but name. It undoes nothing the newer release
	// did; the schema and data it left behind stay.
	DirBehind Direction = "behind"
	// DirSame means the environment was already bound to this release (a
	// re-promote, or a scoped deploy of one component).
	DirSame Direction = "same"
	// DirUnknown means neither commit ancestry nor cut time can place the two,
	// or the promotion before this one is outside the fetched window.
	DirUnknown Direction = "unknown"
)

// EntryKind is what a timeline row is.
type EntryKind string

const (
	// KindPromotion is a ledger promotion: the environment was bound to a release.
	KindPromotion EntryKind = "promotion"
	// KindFailedDeploy is a deploy that failed or timed out.
	KindFailedDeploy EntryKind = "failed_deploy"
)

// Attempt statuses a timeline reports. An attempt that is still running, or
// was abandoned without a report, is not a row: neither is an outcome.
const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusTimedOut  = "timed_out"
)

// Data is everything Assemble reads, already fetched.
type Data struct {
	Env string
	// Promotions are the environment's ledger, OLDEST FIRST: the order
	// that decides what is bound and which moves are rollbacks.
	Promotions []release.Promotion
	// Releases are the project's releases in any order.
	Releases []release.Release
	// Attempts are deploy outcomes: a machine ledger's applies, a control
	// plane's convergences.
	Attempts []Attempt
	// Activity are the events that say who promoted and through which
	// channel. Optional.
	Activity []Activity
	// Truncated says the source stopped before the start of the ledger, so
	// the oldest promotion's predecessor is unknown.
	Truncated bool
	// Warnings are things a source could not read and carried on without.
	Warnings []string
}

// Attempt is one deploy outcome for a promotion.
type Attempt struct {
	ID          string
	PromotionID string
	// Status is StatusSucceeded, StatusFailed or StatusTimedOut.
	Status string
	// StartedAt is zero when the source does not record a start; the
	// promotion's time stands in for it.
	StartedAt  time.Time
	FinishedAt time.Time
	Reason     string
	Cluster    string
}

// Activity is one activity-stream event.
type Activity struct {
	At        time.Time
	Verb      string
	TargetID  string
	ActorName string
	Via       string
	Detail    map[string]string
}

// CommitRange is the source history a promotion moved over.
type CommitRange struct {
	FromRelease string `json:"from_release,omitempty"`
	ToRelease   string `json:"to_release,omitempty"`
	FromCommit  string `json:"from_commit,omitempty"`
	ToCommit    string `json:"to_commit,omitempty"`
	// Reverts: the move is BEHIND, so the commits are the ones the move
	// takes away.
	Reverts bool `json:"reverts,omitempty"`
	// Total is the commit count when known, 0 when not.
	Total     int  `json:"total,omitempty"`
	Truncated bool `json:"truncated,omitempty"`
}

// Rollout is how a deploy attempt ended.
type Rollout struct {
	Status          string `json:"status"`
	DurationSeconds int64  `json:"duration_s,omitempty"`
	FinishedAt      string `json:"finished_at,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Cluster         string `json:"cluster,omitempty"`
}

// Gate phases: what was known before the environment moved, and what was
// learned after.
const (
	PhaseClaimed  = "claimed"
	PhaseRecorded = "recorded"
)

// GateResult is one check's verdict on a promotion.
type GateResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Phase   string `json:"phase"`
	Summary string `json:"summary,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Health is a release-health verdict, when one exists.
type Health struct {
	Verdict string `json:"verdict"`
	Detail  string `json:"detail,omitempty"`
}

// Entry is one row of the timeline.
type Entry struct {
	Kind        EntryKind `json:"kind"`
	Env         string    `json:"env"`
	Version     string    `json:"version"`
	At          time.Time `json:"at"`
	PromotionID string    `json:"promotion_id,omitempty"`
	// BundleID is the rendered bundle the promotion bound, when recorded.
	BundleID string `json:"bundle_id,omitempty"`
	// Live marks the environment's current binding.
	Live            bool      `json:"live,omitempty"`
	Direction       Direction `json:"direction,omitempty"`
	PreviousVersion string    `json:"previous_version,omitempty"`
	// PreviousBundleID is the bundle the environment ran before this
	// promotion, for diffing what the move changed.
	PreviousBundleID string `json:"previous_bundle_id,omitempty"`
	FromEnv          string `json:"from_env,omitempty"`
	Actor            string `json:"actor,omitempty"`
	Via              string `json:"via,omitempty"`
	RunURL           string `json:"run_url,omitempty"`
	Note             string `json:"note,omitempty"`
	// Targets is set when a scoped deploy moved only some components.
	Targets    []string     `json:"targets,omitempty"`
	Superseded bool         `json:"superseded_in_flight,omitempty"`
	Range      *CommitRange `json:"commit_range,omitempty"`
	Rollout    *Rollout     `json:"rollout,omitempty"`
	Gates      []GateResult `json:"gates,omitempty"`
	// GateVerdict is the worst gate result: failed, error, passed or
	// skipped; empty when no gate was recorded.
	GateVerdict string  `json:"gate_verdict,omitempty"`
	Health      *Health `json:"health,omitempty"`
}

// Timeline is an environment's assembled history.
type Timeline struct {
	Env string `json:"env"`
	// Entries are newest first, after the since filter and limit.
	Entries []Entry `json:"entries"`
	// Truncated says there is more history than Entries holds.
	Truncated bool     `json:"truncated,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`

	promos    []Entry // every promotion, oldest first, unfiltered
	failed    []Entry // every failed or timed-out deploy, newest first, unfiltered
	truncated bool    // the source did not reach the ledger's start
}

// Assemble builds the timeline. since (zero for none) drops rows older than
// it, and limit (0 for none) keeps the newest rows; either sets Truncated.
func Assemble(d Data, since time.Time, limit int) Timeline {
	byVersion := make(map[string]release.Release, len(d.Releases))
	for _, r := range d.Releases {
		byVersion[r.Version] = r
	}
	activity := matchActivity(d.Promotions, d.Activity)

	promos := make([]Entry, 0, len(d.Promotions))
	idx := map[string]int{}
	for i, p := range d.Promotions {
		e := Entry{
			Kind:        KindPromotion,
			Env:         d.Env,
			Version:     p.Release,
			At:          p.PromotedAt.UTC(),
			PromotionID: p.ID,
			BundleID:    p.BundleID,
			FromEnv:     p.FromEnv,
			Actor:       firstNonEmpty(p.PromotedBy.Actor, p.PromotedBy.User),
			Via:         p.Run.Provider,
			RunURL:      p.Run.URL,
			Note:        p.Note,
			Targets:     p.Targets,
			Superseded:  p.SupersededInFlight,
		}
		if ev, ok := activity[i]; ok {
			e.Actor = firstNonEmpty(ev.ActorName, e.Actor)
			e.Via = firstNonEmpty(ev.Via, e.Via)
		}
		e.Gates, e.GateVerdict = gateResults(p)

		switch {
		case i == 0 && d.Truncated:
			e.Direction = DirUnknown
		case i == 0:
			e.Direction = DirInitial
			e.Range = recordedRange(byVersion[p.Release], byVersion)
		default:
			prev := d.Promotions[i-1]
			e.PreviousVersion = prev.Release
			e.PreviousBundleID = prev.BundleID
			e.Direction, e.Range = direction(prev.Release, p.Release, d.Releases, byVersion)
		}
		if p.ID != "" {
			idx[p.ID] = len(promos)
		}
		promos = append(promos, e)
	}
	if n := len(promos); n > 0 {
		promos[n-1].Live = true
	}

	var failed []Entry
	best := map[int]Attempt{}
	for _, a := range d.Attempts {
		at, ok := idx[a.PromotionID]
		switch a.Status {
		case StatusSucceeded:
			if ok && a.FinishedAt.After(best[at].FinishedAt) {
				best[at] = a
			}
		case StatusFailed, StatusTimedOut:
			f := Entry{Kind: KindFailedDeploy, Env: d.Env, PromotionID: a.PromotionID, At: a.FinishedAt.UTC()}
			start := a.StartedAt
			if ok {
				p := promos[at]
				f.Version, f.Actor, f.Via = p.Version, p.Actor, p.Via
				if start.IsZero() {
					start = p.At
				}
			}
			if f.At.IsZero() {
				f.At = start.UTC()
			}
			f.Rollout = rollout(a, start)
			failed = append(failed, f)
		}
	}
	for at, a := range best {
		start := a.StartedAt
		if start.IsZero() {
			start = promos[at].At
		}
		promos[at].Rollout = rollout(a, start)
	}
	// Newest first; ties keep ledger order, which is the order that matters.
	sort.SliceStable(failed, func(i, j int) bool { return failed[i].At.After(failed[j].At) })

	entries := make([]Entry, 0, len(promos)+len(failed))
	pi, fi := len(promos)-1, 0
	for pi >= 0 || fi < len(failed) {
		// A deploy that failed at the instant of its promotion came after it.
		if pi < 0 || (fi < len(failed) && !failed[fi].At.Before(promos[pi].At)) {
			entries = append(entries, failed[fi])
			fi++
			continue
		}
		entries = append(entries, promos[pi])
		pi--
	}

	t := Timeline{Env: d.Env, Warnings: d.Warnings, promos: promos, failed: failed, truncated: d.Truncated}
	t.Truncated = d.Truncated
	if !since.IsZero() {
		kept := entries[:0:0]
		for _, e := range entries {
			if !e.At.Before(since) {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	if limit > 0 && len(entries) > limit {
		entries, t.Truncated = entries[:limit], true
	}
	t.Entries = entries
	return t
}

// AttachHealth sets each promotion row's health verdict, keyed by promotion
// id. A row with no verdict says nothing, which is not the same as healthy.
func (t *Timeline) AttachHealth(byPromotion map[string]Health) {
	for i := range t.Entries {
		if h, ok := byPromotion[t.Entries[i].PromotionID]; ok && t.Entries[i].Kind == KindPromotion {
			h := h
			t.Entries[i].Health = &h
		}
	}
	for i := range t.promos {
		if h, ok := byPromotion[t.promos[i].PromotionID]; ok {
			h := h
			t.promos[i].Health = &h
		}
	}
}

// FailuresOf returns the failed and timed-out deploys of one promotion, newest
// first. A promotion whose deploy never landed has no rollout of its own; its
// failures are rows of their own in the timeline, and this is how `show`
// finds the ones that belong to the promotion it is showing.
func (t Timeline) FailuresOf(promotionID string) []Entry {
	var out []Entry
	for _, f := range t.failed {
		if promotionID != "" && f.PromotionID == promotionID {
			out = append(out, f)
		}
	}
	return out
}

// ErrNotFound is returned by Find when no promotion matches.
var ErrNotFound = errors.New("no such promotion or release in this environment's timeline")

// Find resolves ref to a promotion: a promotion id, else the newest promotion
// of the release version ref names.
func (t Timeline) Find(ref string) (Entry, error) {
	for i := len(t.promos) - 1; i >= 0; i-- {
		if t.promos[i].PromotionID != "" && t.promos[i].PromotionID == ref {
			return t.promos[i], nil
		}
	}
	for i := len(t.promos) - 1; i >= 0; i-- {
		if t.promos[i].Version == ref {
			return t.promos[i], nil
		}
	}
	return Entry{}, fmt.Errorf("%w: %q", ErrNotFound, ref)
}

func rollout(a Attempt, start time.Time) *Rollout {
	r := &Rollout{Status: a.Status, Reason: a.Reason, Cluster: a.Cluster}
	if !a.FinishedAt.IsZero() {
		r.FinishedAt = a.FinishedAt.UTC().Format(time.RFC3339)
		if d := a.FinishedAt.Sub(start); !start.IsZero() && d > 0 {
			r.DurationSeconds = int64(d / time.Second)
		}
	}
	return r
}

// direction places `to` relative to `from`: commit ancestry when the recorded
// ranges chain, else cut time, else unknown. Version labels are never
// compared: v1.7.40 and 20261014.042859-cd678a0 share no order.
func direction(from, to string, all []release.Release, by map[string]release.Release) (Direction, *CommitRange) {
	if from == to {
		return DirSame, nil
	}
	fr, okF := by[from]
	tr, okT := by[to]
	if !okF || !okT {
		return DirUnknown, nil
	}
	cmp := release.CompareCommits(fr, tr, all)
	dir := DirUnknown
	switch {
	case cmp.State == release.CommitsComputed && cmp.Reverts:
		dir = DirBehind
	case cmp.State == release.CommitsComputed:
		dir = DirAhead
	case tr.CreatedAt.After(fr.CreatedAt):
		dir = DirAhead
	case tr.CreatedAt.Before(fr.CreatedAt):
		dir = DirBehind
	}
	rng := &CommitRange{
		FromRelease: from, ToRelease: to,
		FromCommit: fr.Git.Commit, ToCommit: tr.Git.Commit,
		Reverts: dir == DirBehind,
	}
	if cmp.State == release.CommitsComputed {
		rng.Total, rng.Truncated = cmp.Total, cmp.Truncated
	} else {
		rng.Truncated = true
	}
	return dir, rng
}

// recordedRange is a release's own range, for an environment's first
// promotion.
func recordedRange(r release.Release, _ map[string]release.Release) *CommitRange {
	if r.Version == "" || r.Commits == nil {
		return nil
	}
	return &CommitRange{
		FromRelease: r.Commits.BaseRelease, ToRelease: r.Version,
		FromCommit: r.Commits.BaseCommit, ToCommit: r.Git.Commit,
		Total: r.Commits.Total, Truncated: r.Commits.Truncated,
	}
}

func gateResults(p release.Promotion) ([]GateResult, string) {
	var out []GateResult
	add := func(gs []release.Gate, phase string) {
		for _, g := range gs {
			out = append(out, GateResult{
				Name: g.Name, Status: string(release.StageStatusOfGate(g)),
				Phase: phase, Summary: g.Summary, URL: g.URL,
			})
		}
	}
	add(p.Gates, PhaseClaimed)
	add(p.RecordedGates, PhaseRecorded)
	return out, gateVerdict(out)
}

func gateVerdict(gs []GateResult) string {
	has := map[string]bool{}
	for _, g := range gs {
		has[g.Status] = true
	}
	switch {
	case has[string(release.StageFailed)]:
		return string(release.StageFailed)
	case has[string(release.StageErrored)]:
		return string(release.StageErrored)
	case has[string(release.StagePassed)]:
		return string(release.StagePassed)
	case has[string(release.StageSkipped)]:
		return string(release.StageSkipped)
	}
	return ""
}

// activityWindow is how far an activity event may sit from a promotion and
// still be taken for the one that wrote it, when it names no promotion id.
const activityWindow = 2 * time.Minute

// matchActivity joins promote events to promotions (by index): by the
// promotion id in the event's detail first, then by release and time.
func matchActivity(ps []release.Promotion, events []Activity) map[int]Activity {
	out := map[int]Activity{}
	claimed := map[int]bool{}
	for i, p := range ps {
		if p.ID == "" {
			continue
		}
		for j, ev := range events {
			if ev.Verb != "promote" || claimed[j] {
				continue
			}
			if ev.Detail["promotion_id"] == p.ID || ev.Detail["promotion.id"] == p.ID || ev.Detail["id"] == p.ID {
				out[i], claimed[j] = ev, true
				break
			}
		}
	}
	for i, p := range ps {
		if _, done := out[i]; done {
			continue
		}
		best, bestGap := -1, activityWindow+1
		for j, ev := range events {
			if ev.Verb != "promote" || claimed[j] || ev.TargetID != p.Release {
				continue
			}
			gap := ev.At.Sub(p.PromotedAt)
			if gap < 0 {
				gap = -gap
			}
			if gap <= activityWindow && gap < bestGap {
				best, bestGap = j, gap
			}
		}
		if best >= 0 {
			out[i], claimed[best] = events[best], true
		}
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
