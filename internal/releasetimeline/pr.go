package releasetimeline

import (
	"regexp"
	"strconv"

	"github.com/reliant-labs/forge/pkg/release"
)

// A squash merge appends " (#123)" to the title, so the number is the LAST
// thing on the subject. Anchoring to the end is what keeps a revert right:
// `Revert "fix a (#12)" (#15)` is pull request 15, and 12 is only mentioned.
var (
	squashPR = regexp.MustCompile(`\(#(\d{1,9})\)\s*$`)
	mergePR  = regexp.MustCompile(`^Merge pull request #(\d{1,9})\b`)
)

// PRNumber extracts the pull request a commit subject was merged as: the
// trailing "(#123)" of a squash merge, or "Merge pull request #123 from ..." of
// a merge commit. ok is false for a subject that names neither.
func PRNumber(subject string) (n int, ok bool) {
	for _, re := range []*regexp.Regexp{squashPR, mergePR} {
		if m := re.FindStringSubmatch(subject); m != nil {
			n, err := strconv.Atoi(m[1])
			return n, err == nil
		}
	}
	return 0, false
}

// PRNumbers returns the pull requests behind commits, in commit order, each
// once. A commit that is not a recognisable merge contributes nothing.
func PRNumbers(commits []release.Commit) []int {
	var out []int
	seen := map[int]bool{}
	for _, c := range commits {
		n, ok := PRNumber(c.Subject)
		if !ok || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}
