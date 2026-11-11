package releasehealth

// ATTRIBUTION (forge#b7e1113e). A release answers only for what it did, so a
// finding counts against it only when the after window measured it:
//
//   - The window is the live release's. A promotion is intent; its release is
//     live when an attempt shipped it, which a promotion whose first attempt
//     failed reaches only on a later attempt, under the same promotion id
//     ([LiveAt]). A window that ended before then measured the release before
//     it, and judges nothing ([Report.WindowNotLive]).
//   - The release served the window. Below [MinVersionShare] of the after
//     window's measured traffic, the rollout had not happened, or had not
//     finished, there ([Report.RolloutIncomplete]).
//   - The finding is the release's. Only VERSION-SPECIFIC evidence takes a
//     finding off the release: a service's figures are not its when that
//     service's own traffic was mostly another version's; an issue is not
//     its when every one of its after-window events is tagged another
//     version ([Report.IssueWindowVersions]). A low aggregate share alone moves no
//     finding: an alert or a restart carries no version, and a release on a
//     tenth of the traffic can still take the env down, so they stay the
//     release's (a critical alert keeps failing it).
//
// A finding that fails none of these keeps its policy. One that fails any is
// kept in the report with policy warn and says why in [Finding.Unattributed],
// so it never fails the release. When the window is not the live release's,
// or its rollout was incomplete, nothing in it proves the release good either:
// the verdict is INCONCLUSIVE, never PASS.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// MinVersionShare is the least share (0..1) of the after window's measured
// traffic the judged release must serve for the window to be its.
const MinVersionShare = 0.5

// LiveState says what a promotion's deploy gates say about when its release
// went live ([LiveAt]).
type LiveState string

const (
	// LiveUnknown: the promotion records no apply or cluster-rollout gate,
	// so nothing says when it went live; its promoted_at stands in.
	LiveUnknown LiveState = "unknown"
	// LiveShipped: an attempt shipped the release, at the time LiveAt returns.
	LiveShipped LiveState = "shipped"
	// LiveNotShipped: the promotion records deploy gates, and every attempt
	// so far failed: its release has not run yet.
	LiveNotShipped LiveState = "not_shipped"
)

// ApplyGateName is the gate `forge env deploy` records on a promotion once an
// attempt has applied it: the evidence every machine reads.
const ApplyGateName = "apply"

// shipGates are the gates whose verdicts say whether an attempt shipped.
var shipGates = []string{ApplyGateName, release.ClusterRolloutGateName}

// LiveAt is when the release of a promotion went live, from the promotion's
// gates (pre- and post-promote, in any order): the end of the FIRST attempt
// whose apply and cluster-rollout gates all succeeded (passed, or skipped by a
// deploy that did not wait), its end being the latest of those gates. An
// attempt is the gates sharing a run id; a gate without one is an attempt of
// its own. A failed attempt shipped nothing, so a promotion whose first
// attempt failed goes live on a later one, under the same promotion id; a
// re-apply after it shipped does not move it.
//
// It is exported for every judge of a release: a control plane anchors the
// after window at it ([Plan]'s promotedAt) rather than at the promotion's
// first promoted_at.
func LiveAt(gates []release.Gate) (time.Time, LiveState) {
	type attempt struct {
		failed bool
		end    time.Time
	}
	var order []string
	attempts := map[string]*attempt{}
	for i, g := range gates {
		if !slices.Contains(shipGates, g.Name) {
			continue
		}
		key := g.RunID
		if key == "" {
			key = fmt.Sprintf("\x00%d", i)
		}
		a := attempts[key]
		if a == nil {
			a = &attempt{}
			attempts[key] = a
			order = append(order, key)
		}
		switch g.Status {
		case release.GateStatusPassed, release.GateStatusSkipped:
		default:
			a.failed = true
		}
		if end := gateEnd(g); end.After(a.end) {
			a.end = end
		}
	}
	if len(order) == 0 {
		return time.Time{}, LiveUnknown
	}
	var live time.Time
	for _, k := range order {
		a := attempts[k]
		if a.failed || a.end.IsZero() {
			continue
		}
		if live.IsZero() || a.end.Before(live) {
			live = a.end
		}
	}
	if live.IsZero() {
		return time.Time{}, LiveNotShipped
	}
	return live.UTC(), LiveShipped
}

// gateEnd is when a gate's check finished, else when it was recorded, else
// when it started.
func gateEnd(g release.Gate) time.Time {
	for _, t := range []*time.Time{g.FinishedAt, g.RecordedAt, g.StartedAt} {
		if t != nil && !t.IsZero() {
			return *t
		}
	}
	return time.Time{}
}

// WithLiveAt records when the judged release went live ([LiveAt]) and
// attributes the report against it ([Attribute]): an after window that ended
// before the release went live, or of a promotion none of whose attempts has
// shipped, is not the release's ([Report.WindowNotLive]). A caller that judges
// a window it did not plan (`forge obs check` over a control plane's answer)
// applies it; a judge that anchors its windows at the live time finds nothing
// to change.
func WithLiveAt(r Report, at time.Time, state LiveState) Report {
	out := r
	switch state {
	case LiveShipped:
		out.Subject.LiveAt = at.UTC()
		if !r.After.To.IsZero() && !at.Before(r.After.To) {
			out.WindowNotLive = fmt.Sprintf("the judged after window [%s, %s) ended before the release went live at %s (its deploy gates), so it measured the release before it",
				stamp(r.After.From), stamp(r.After.To), stamp(at))
		}
	case LiveNotShipped:
		out.WindowNotLive = "no attempt of this promotion has shipped its release yet (every apply or cluster-rollout gate it records failed), so the after window measured the release before it"
	}
	return Attribute(out)
}

// Attribute applies the attribution rule (above) to a judged report: each
// finding the after window cannot pin on the judged release gets policy warn
// and a reason in Unattributed, the release's version share is recorded, and
// the verdict is recomputed. A report it changes nothing in is returned as
// judged. It is idempotent, and [Evaluate] applies it.
func Attribute(r Report) Report {
	out := r
	out.Findings = slices.Clone(r.Findings)
	out.VersionShare = nil
	if share, ok := releaseShare(r.Services); ok {
		out.VersionShare = &share
		if share < MinVersionShare && out.RolloutIncomplete == "" {
			out.RolloutIncomplete = fmt.Sprintf("the judged release served %s of the after window's measured traffic (under %s): its rollout had not happened, or had not finished, in that window",
				pct(share), pct(MinVersionShare))
		}
	}
	changed := out.RolloutIncomplete != r.RolloutIncomplete
	for i, f := range out.Findings {
		if f.Unattributed != "" {
			continue
		}
		why, otherVersion := out.unattributed(f)
		if why == "" {
			continue
		}
		// A failing finding pinned on another version is that version's: it
		// neither fails nor holds the release. One the window merely cannot
		// pin on it (a service its rollout had not reached) leaves the
		// release unproven.
		if f.Policy == PolicyFail && !otherVersion && out.RolloutIncomplete == "" && out.WindowNotLive == "" {
			out.RolloutIncomplete = fmt.Sprintf("the rollout was incomplete in the after window: %s", why)
		}
		out.Findings[i].Policy, out.Findings[i].Unattributed = PolicyWarn, why
		changed = true
	}
	if !changed && r.WindowNotLive == "" {
		out.Verdict = r.Verdict
		return out
	}
	out.Verdict = verdictOf(out.Findings, out.AfterComplete, out.Services, minRequestsOf(out.Thresholds), out.unproven())
	return out
}

// unattributed says why f is not the judged release's doing, or "" when it
// is; otherVersion is true when the evidence pins it on another version (the
// issue's events), not merely off this one. Every reason but a window that
// is not the live release's is evidence about f's own version: the release's
// aggregate share never moves a finding on its own.
func (r Report) unattributed(f Finding) (why string, otherVersion bool) {
	if r.WindowNotLive != "" {
		return "the after window is not the live release's", false
	}
	rel := r.Subject.Release
	switch f.Code {
	case release.HealthErrorRate, release.HealthLatency:
		s, ok := r.service(f.Service)
		if !ok {
			return "", false
		}
		if s.VersionShare != nil && *s.VersionShare < MinVersionShare {
			return fmt.Sprintf("%s served only %s of %s's after-window traffic", nameOr(rel, "the judged release"), pct(*s.VersionShare), f.Service), false
		}
	case release.HealthNewIssue, release.HealthRegressedIssue:
		is, ok := r.issue(f.Code, f.Ref, f.Message)
		if !ok || rel == "" {
			return "", false
		}
		if versions := r.IssueWindowVersions[is.ID]; len(versions) > 0 {
			if versions[rel] > 0 {
				return "", false // an event of the release is among them: its own
			}
			return fmt.Sprintf("its %d after-window event(s) are all tagged %s, not %s", sumEvents(versions), strings.Join(versionsOf(versions), ", "), rel), true
		}
		if f.Code == release.HealthNewIssue && is.FirstVersion != "" && is.FirstVersion != rel {
			return fmt.Sprintf("first seen in %s, not %s", is.FirstVersion, rel), true
		}
		// No version-specific evidence: the issue stays the release's.
	case release.HealthAlertFiring, release.HealthWorkloadRestarts:
		// An alert carries no version, and a restart is not tied to the
		// version its workload ran, so neither is ever taken off the
		// release by share: a critical alert keeps failing it. Only a window
		// that is not the live release's (above) moves them.
	}
	return "", false
}

func (r Report) service(name string) (ServiceHealth, bool) {
	for _, s := range r.Services {
		if s.Service == name {
			return s, true
		}
	}
	return ServiceHealth{}, false
}

// issue finds a finding's issue by its ref; a finding that crossed a wire
// without its ref is matched by its message, which names the issue id.
func (r Report) issue(code, ref, message string) (Issue, bool) {
	list := r.NewIssues
	if code == release.HealthRegressedIssue {
		list = r.RegressedIssues
	}
	for _, is := range list {
		if is.ID != "" && (is.ID == ref || (ref == "" && strings.Contains(message, "issue "+is.ID+" "))) {
			return is, true
		}
	}
	return Issue{}, false
}

// releaseShare is the judged release's share of the after window's traffic,
// weighted by requests, over the services whose share was measured.
func releaseShare(services []ServiceHealth) (float64, bool) {
	var served, total float64
	for _, s := range services {
		if s.VersionShare == nil || s.After.Requests <= 0 {
			continue
		}
		served += *s.VersionShare * float64(s.After.Requests)
		total += float64(s.After.Requests)
	}
	if total == 0 {
		return 0, false
	}
	return served / total, true
}

func sumEvents(m map[string]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}

func versionsOf(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for v := range m {
		if v == "" {
			v = "no version"
		}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func nameOr(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
