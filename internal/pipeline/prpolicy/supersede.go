package prpolicy

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// PRState is a pull request's GitHub state.
type PRState string

const (
	PRStateOpen   PRState = "open"
	PRStateMerged PRState = "merged"
	// PRStateClosed is closed without merging.
	PRStateClosed PRState = "closed"
)

// PRSummary is what supersession detection knows of one pull request. The
// caller fills it from the projected PR records and git; nothing here reads
// a title.
type PRSummary struct {
	// Repo is owner/name; PRs of different repositories never supersede one
	// another.
	Repo   string
	Number int
	// Author is the GitHub login that opened the PR.
	Author string
	State  PRState
	// BaseRef is the branch the PR targets. A backport of the same patch to
	// another branch is not a duplicate.
	BaseRef string
	HeadSHA string
	// PatchID is `git patch-id --stable` of the PR's whole diff against its
	// merge base; empty when unknown or the diff is empty, which is never
	// evidence.
	PatchID string
	// Commits are the shas from the merge base (exclusive) to the head
	// (inclusive).
	Commits []string
	// Body is the PR description, read only for "supersedes #n" links.
	Body string
	// Reverted is true for a merged PR whose landed commit has since been
	// reverted on the base: its change is no longer there, so it supersedes
	// nothing (a re-land after a revert carries the same patch).
	Reverted bool
}

// SupersessionKind is what DetectSupersession found.
type SupersessionKind string

const (
	SupersessionNone SupersessionKind = "none"
	// SupersessionSuperseded: another PR already contains this one's change,
	// merged, or open and declared by the same author.
	SupersessionSuperseded SupersessionKind = "superseded"
	// SupersessionDuplicate: an older open PR carries the same change.
	SupersessionDuplicate SupersessionKind = "duplicate"
)

// Supersession is DetectSupersession's answer. By is the PR that contains
// the change; zero when Kind is none.
type Supersession struct {
	Kind SupersessionKind
	By   int
}

// EvidenceKind is the deterministic fact a supersession rests on.
type EvidenceKind string

const (
	// EvidencePatchID: the two PRs' diffs have the same stable patch-id.
	EvidencePatchID EvidenceKind = "patch_id"
	// EvidenceSameHead: the two PRs have the same head commit.
	EvidenceSameHead EvidenceKind = "same_head"
	// EvidenceAncestry: this PR's head commit is in the other PR's commits.
	EvidenceAncestry EvidenceKind = "ancestry"
	// EvidenceLink: the other PR's body says "supersedes #n", and the two PRs
	// have the same author.
	EvidenceLink EvidenceKind = "link"
)

// SupersessionEvidence is one cited fact, for `pr.close@1` to post.
type SupersessionEvidence struct {
	Kind   EvidenceKind
	PR     int
	Detail string
}

// supersedesRe finds "supersedes #12" and "Supersedes: #12, #13 and #14".
var (
	supersedesRe = regexp.MustCompile(`(?i)\bsupersedes:?\s+(#\d+(?:\s*(?:,|and|&)\s*#\d+)*)`)
	prRefRe      = regexp.MustCompile(`#(\d+)`)
)

// SupersedesLinks returns the PR numbers a body declares it supersedes, in
// order of first appearance.
func SupersedesLinks(body string) []int {
	var out []int
	for _, m := range supersedesRe.FindAllStringSubmatch(body, -1) {
		for _, ref := range prRefRe.FindAllStringSubmatch(m[1], -1) {
			n, err := strconv.Atoi(ref[1])
			if err == nil && n > 0 && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// DetectSupersession decides whether an open PR should be closed because
// another PR already carries its change (design §6.1), and cites the
// evidence. It never guesses: only these count, and only between PRs of the
// same repository and base branch:
//
//   - superseded by a merged PR (not since reverted) with the same patch-id
//     or head, or whose commits contain this PR's head;
//   - superseded by an open or merged PR whose body links "supersedes #n" to
//     this PR, opened by the same author (a link from anyone else is not
//     evidence: it would let one author close another's PR; two open PRs
//     linking each other are not evidence either);
//   - a duplicate of an older (lower-numbered) open PR with the same
//     patch-id or head, unless this PR's own author declared it supersedes
//     that one.
//
// An open PR whose commits contain this PR's head is not evidence on its
// own: that is a stack, where this PR is meant to land first. Titles are
// never read.
//
// When several PRs qualify, a merged one wins over an open one, then
// superseded over duplicate, then the lowest number. The evidence returned
// is every fact against the chosen PR.
func DetectSupersession(pr PRSummary, others []PRSummary) (Supersession, []SupersessionEvidence) {
	none := Supersession{Kind: SupersessionNone}
	if pr.State != PRStateOpen {
		return none, nil
	}
	ownLinks := SupersedesLinks(pr.Body)
	type candidate struct {
		s        Supersession
		merged   bool
		evidence []SupersessionEvidence
	}
	var cands []candidate
	for _, o := range others {
		if o.Number == pr.Number || !strings.EqualFold(o.Repo, pr.Repo) || o.BaseRef != pr.BaseRef {
			continue
		}
		var ev []SupersessionEvidence
		samePatch := pr.PatchID != "" && o.PatchID == pr.PatchID
		sameHead := pr.HeadSHA != "" && o.HeadSHA == pr.HeadSHA
		if samePatch {
			ev = append(ev, SupersessionEvidence{Kind: EvidencePatchID, PR: o.Number, Detail: fmt.Sprintf("#%d and #%d have the same patch-id %s", pr.Number, o.Number, pr.PatchID)})
		}
		if sameHead {
			ev = append(ev, SupersessionEvidence{Kind: EvidenceSameHead, PR: o.Number, Detail: fmt.Sprintf("#%d and #%d have the same head %s", pr.Number, o.Number, pr.HeadSHA)})
		}
		linked := slices.Contains(SupersedesLinks(o.Body), pr.Number) && o.Author != "" && strings.EqualFold(o.Author, pr.Author)
		if linked {
			ev = append(ev, SupersessionEvidence{Kind: EvidenceLink, PR: o.Number, Detail: fmt.Sprintf("#%d, by the same author %s, says it supersedes #%d", o.Number, o.Author, pr.Number)})
		}
		switch {
		case o.State == PRStateMerged && !o.Reverted:
			if !sameHead && pr.HeadSHA != "" && slices.Contains(o.Commits, pr.HeadSHA) {
				ev = append(ev, SupersessionEvidence{Kind: EvidenceAncestry, PR: o.Number, Detail: fmt.Sprintf("merged #%d contains #%d's head %s", o.Number, pr.Number, pr.HeadSHA)})
			}
			if len(ev) > 0 {
				cands = append(cands, candidate{Supersession{SupersessionSuperseded, o.Number}, true, ev})
			}
		case o.State == PRStateOpen:
			switch {
			case linked && !slices.Contains(ownLinks, o.Number):
				// Two open PRs that each claim to supersede the other
				// prove nothing; neither is closed on the link.
				cands = append(cands, candidate{Supersession{SupersessionSuperseded, o.Number}, false, ev})
			case (samePatch || sameHead) && o.Number < pr.Number && !slices.Contains(ownLinks, o.Number):
				cands = append(cands, candidate{Supersession{SupersessionDuplicate, o.Number}, false, ev})
			}
		}
	}
	if len(cands) == 0 {
		return none, nil
	}
	best := slices.MinFunc(cands, func(a, b candidate) int {
		if a.merged != b.merged {
			if a.merged {
				return -1
			}
			return 1
		}
		if a.s.Kind != b.s.Kind {
			if a.s.Kind == SupersessionSuperseded {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.s.By, b.s.By)
	})
	return best.s, best.evidence
}
