package prpolicy

import (
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Severity is a review finding's severity.
type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityNit     Severity = "nit"
)

// Finding is one review finding: the shape of reliant.v1.ReviewFinding, a
// review node's output, and `pr_head_verdicts.findings_json` (whose keys
// are these JSON names).
type Finding struct {
	// ID is stable across review rounds: a later round reports a finding
	// that is still there under the same id, and leaves out one that is
	// fixed.
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	// Path is the repository-relative file the finding is about, when it is
	// about one.
	Path string `json:"path,omitempty"`
	// Line is the 1-based line in Path, when the finding names one.
	Line int    `json:"line,omitempty"`
	Body string `json:"body"`
}

// FindingFromProto converts a review node's finding.
func FindingFromProto(f *reliantv1.ReviewFinding) Finding {
	return Finding{
		ID:       f.GetId(),
		Severity: Severity(f.GetSeverity()),
		Path:     f.GetPath(),
		Line:     int(f.GetLine()),
		Body:     f.GetBody(),
	}
}

// FindingsFromProto converts a review node's findings.
func FindingsFromProto(fs []*reliantv1.ReviewFinding) []Finding {
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		out = append(out, FindingFromProto(f))
	}
	return out
}

// isBlocker reports whether f blocks by its own severity. Anything that is
// not a nit counts as a blocker, so a malformed severity fails closed.
func (f Finding) isBlocker() bool {
	return Severity(strings.ToLower(strings.TrimSpace(string(f.Severity)))) != SeverityNit
}

// BlockReason is why a blocker blocks.
type BlockReason string

const (
	// BlockFirstRound: the PR's first review round, where every blocker
	// blocks.
	BlockFirstRound BlockReason = "first_round"
	// BlockUnfixed: a blocker of the prior round, still there.
	BlockUnfixed BlockReason = "unfixed"
	// BlockNewCode: a blocker in code changed since the prior round's head.
	BlockNewCode BlockReason = "new_code"
)

// Blocker is a finding that blocks the head, and why.
type Blocker struct {
	Finding
	Reason BlockReason
	// Why says it in plain words.
	Why string
}

// NewCode is the code a review round may block on for the first time.
type NewCode struct {
	// All is true on a PR's first review round: there is no prior round, so
	// the whole PR is new code and every blocker blocks.
	All bool
	// Paths are the repository-relative files changed between the prior
	// round's head and this round's.
	Paths []string
}

// FirstRound is the NewCode of a PR's first review round.
func FirstRound() NewCode { return NewCode{All: true} }

// ChangedSince is the NewCode of a later round: the files changed since the
// prior round's head.
func ChangedSince(paths ...string) NewCode { return NewCode{Paths: paths} }

// touches reports whether a finding about p is about new code. A finding
// may name a directory, which is new code when a file under it is.
func (n NewCode) touches(p string) bool {
	if p == "" {
		return false
	}
	p = normPath(p)
	for _, c := range n.Paths {
		c = normPath(c)
		if c == p || strings.HasPrefix(c, p+"/") {
			return true
		}
	}
	return false
}

// Decision is what [Converge] decided about one review round.
type Decision struct {
	// Blocking are the blockers that block this head, in the order the
	// round reported them.
	Blocking []Blocker
	// Late are blockers raised for the first time on code that did not
	// change since the prior round. They do not block, and Findings records
	// them as nits.
	Late []Finding
	// Fixed are the prior round's blockers this round no longer reports.
	Fixed []Finding
	// Findings are the round's findings as they count: a late finding's
	// severity is nit. Record these, not the reviewer's own list, as the
	// round's findings_json and pass them to the next round as its prior,
	// so a late finding stays a nit in every later round.
	Findings []Finding
}

// Blocks reports whether the head is blocked.
func (d Decision) Blocks() bool { return len(d.Blocking) > 0 }

// Converge decides which of a review round's findings block, so a review
// converges (the owner's "review converges", 2026-11-20). prior are the
// previous round's findings as it recorded them (its Decision.Findings);
// current are this round's, as the reviewer reported them.
//
// A blocker blocks when it is in the PR's first round, when it is a prior
// blocker still there (the same id), or when it is in code changed since the
// prior round's head (its path is in newCode). Any other blocker is late: it
// is about code the previous round already read and passed over, so it does
// not hold the PR another round, and is recorded as a nit. A blocker with no
// path in a later round is late too: nothing ties it to the new code.
//
// Converge keeps the first finding of a repeated id. Ids are compared as
// they are, and an empty id never matches a prior finding.
func Converge(prior, current []Finding, newCode NewCode) Decision {
	priorBlockers := map[string]bool{}
	for _, f := range prior {
		if f.ID != "" && f.isBlocker() {
			priorBlockers[f.ID] = true
		}
	}

	var d Decision
	seen := map[string]bool{}
	for _, f := range current {
		if f.ID != "" {
			if seen[f.ID] {
				continue
			}
			seen[f.ID] = true
		}
		if !f.isBlocker() {
			d.Findings = append(d.Findings, f)
			continue
		}
		switch {
		case newCode.All:
			d.Blocking = append(d.Blocking, Blocker{Finding: f, Reason: BlockFirstRound,
				Why: "a blocker in the PR's first review round"})
		case f.ID != "" && priorBlockers[f.ID]:
			d.Blocking = append(d.Blocking, Blocker{Finding: f, Reason: BlockUnfixed,
				Why: fmt.Sprintf("blocker %s was raised in the previous round and is still there", f.ID)})
		case newCode.touches(f.Path):
			d.Blocking = append(d.Blocking, Blocker{Finding: f, Reason: BlockNewCode,
				Why: fmt.Sprintf("%s changed since the previous round's head", f.Path)})
		default:
			d.Late = append(d.Late, f)
			late := f
			late.Severity = SeverityNit
			d.Findings = append(d.Findings, late)
			continue
		}
		d.Findings = append(d.Findings, f)
	}

	for _, f := range prior {
		if f.ID != "" && f.isBlocker() && !seen[f.ID] {
			d.Fixed = append(d.Fixed, f)
		}
	}
	return d
}

// LateFindingNote says, in plain words, why a late finding did not block.
func LateFindingNote(f Finding) string {
	where := "code"
	if f.Path != "" {
		where = f.Path
	}
	return fmt.Sprintf("%s was raised for the first time on %s, which did not change since the previous round; "+
		"recorded as a nit so the review converges", findingName(f), where)
}

func findingName(f Finding) string {
	if f.ID != "" {
		return "finding " + f.ID
	}
	return "a finding"
}
