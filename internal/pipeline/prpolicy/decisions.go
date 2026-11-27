package prpolicy

import (
	"slices"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// ApprovalConditions carries the results of the owner_approval conditions
// this package cannot evaluate itself, keyed by the entry's index in the
// Resolved OwnerApproval list: WhenCmd[i] is true when entry i's when_cmd
// exited 0 on the head (run in the credentials-none sandbox), and When[i] is
// true when entry i's CEL `when` evaluated to true over the subject. An
// absent key means the caller did not evaluate it; see RequiredDecisions.
type ApprovalConditions struct {
	WhenCmd map[int]bool
	When    map[int]bool
}

// RequiredDecision is a decision a person must record in the app before the
// head lands. It binds to Head: a push voids it, and the new head is judged
// afresh.
type RequiredDecision struct {
	Kind config.ApprovalKind
	Head string
	// Entries are the indexes of the owner_approval entries that require it.
	Entries []int
	// Paths are the changed paths that matched those entries' globs, sorted;
	// empty when the requiring entries name no paths.
	Paths []string
	// Unevaluated is true when a requiring entry's when_cmd or when had no
	// result: it is required because it could not be shown not to be.
	Unevaluated bool
}

// PendingConditions lists the owner_approval entries whose when_cmd or when
// the caller must evaluate before calling RequiredDecisions: those whose
// subject and paths apply, so their result can change the answer. An entry
// whose paths match nothing is not listed, so its when_cmd never runs.
func PendingConditions(changedPaths []string, incidentSource IncidentSource, approvals []config.OwnerApproval) (whenCmd, when []int) {
	for i, a := range approvals {
		if !approvalSubjectApplies(a, incidentSource) {
			continue
		}
		if _, ok := matchPaths(a.Paths, changedPaths); !ok {
			continue
		}
		if a.WhenCmd != "" {
			whenCmd = append(whenCmd, i)
		}
		if a.When != "" {
			when = append(when, i)
		}
	}
	return whenCmd, when
}

// RequiredDecisions returns the decisions the owner_approval entries require
// of a head, one per kind, in config.ApprovalKinds order (design §4.3, the
// R6a rules on config.OwnerApproval). An entry requires its kind when all of
// its conditions hold (AND): its subject applies, its paths (when it names
// any) match a changed path, its when_cmd (when set) exited 0 and its when
// (when set) is true. Entries of one kind combine with OR.
//
// The subject of approve_prod_fix is an incident fix, so such an entry
// applies only when incidentSource is set (the source of the incident whose
// fix_record_id is this PR); its `when` narrows by source. The other kinds
// apply to every head.
//
// A condition the caller did not evaluate fails closed: the entry requires
// its decision, marked Unevaluated, rather than being silently skipped.
func RequiredDecisions(head string, changedPaths []string, incidentSource IncidentSource, approvals []config.OwnerApproval, conds ApprovalConditions) []RequiredDecision {
	byKind := map[config.ApprovalKind]*RequiredDecision{}
	for i, a := range approvals {
		if !approvalSubjectApplies(a, incidentSource) {
			continue
		}
		matched, ok := matchPaths(a.Paths, changedPaths)
		if !ok {
			continue
		}
		unevaluated := false
		if a.WhenCmd != "" {
			res, have := conds.WhenCmd[i]
			if have && !res {
				continue
			}
			unevaluated = unevaluated || !have
		}
		if a.When != "" {
			res, have := conds.When[i]
			if have && !res {
				continue
			}
			unevaluated = unevaluated || !have
		}
		d := byKind[a.Kind]
		if d == nil {
			d = &RequiredDecision{Kind: a.Kind, Head: head}
			byKind[a.Kind] = d
		}
		d.Entries = append(d.Entries, i)
		d.Paths = append(d.Paths, matched...)
		d.Unevaluated = d.Unevaluated || unevaluated
	}
	var out []RequiredDecision
	for _, k := range config.ApprovalKinds {
		if d := byKind[k]; d != nil {
			slices.Sort(d.Paths)
			d.Paths = slices.Compact(d.Paths)
			out = append(out, *d)
		}
	}
	return out
}

// ApprovedDecision is an approved (or applied) decision recorded against a
// PR head.
type ApprovedDecision struct {
	Kind config.ApprovalKind
	Head string
}

// UnmetDecisions returns the required decisions no approved decision meets.
// A decision meets a requirement only when its kind and head both match: an
// approval of an earlier head does not carry to a new one.
func UnmetDecisions(required []RequiredDecision, approved []ApprovedDecision) []RequiredDecision {
	var out []RequiredDecision
	for _, r := range required {
		if !slices.Contains(approved, ApprovedDecision{Kind: r.Kind, Head: r.Head}) || r.Head == "" {
			out = append(out, r)
		}
	}
	return out
}

func approvalSubjectApplies(a config.OwnerApproval, incidentSource IncidentSource) bool {
	if a.Kind == config.ApproveProdFix {
		return incidentSource != ""
	}
	return true
}

// matchPaths reports whether an entry's globs select the change, and which
// changed paths they matched. An entry with no globs selects every change.
// A malformed glob (config validation rejects them) matches nothing.
func matchPaths(globs, changed []string) ([]string, bool) {
	if len(globs) == 0 {
		return nil, true
	}
	var matched []string
	for _, p := range changed {
		for _, g := range globs {
			if ok, err := doublestar.Match(g, p); err == nil && ok {
				matched = append(matched, p)
				break
			}
		}
	}
	return matched, len(matched) > 0
}
