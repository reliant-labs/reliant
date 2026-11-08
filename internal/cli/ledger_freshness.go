package cli

// "Is this checkout's copy of the ledger stale?" — a question that no longer
// has a subject.
//
// IT USED TO. A self-managed env recorded its promotions in
// .forge/promotions/<env>.jsonl, COMMITTED TO GIT, so "what is prod supposed
// to run" was answered by whatever commit this checkout happened to have. A
// checkout that had not pulled the latest `chore(ledger): record vX` commit
// held an older promotion, and every verdict computed against it was wrong in
// a way the verdict could not show:
//
//   - the cluster runs the new release → every image reports DRIFT from the
//     old one, and an operator chases a deploy that went fine;
//   - the cluster ALSO runs the old release (the new one never deployed) →
//     MATCH, and the gate goes green on exactly the failure it exists for.
//
// Moving the ledger out of the checkout removes the staleness rather than
// detecting it. Neither store can be behind: a control plane IS the ledger,
// and the machine ledger is one directory per PROJECT outside every
// checkout, so all of a project's worktrees read the same file whatever
// branch they are on. That was one of the three reasons for the move.
//
// What remains here is the vocabulary (the enum, its JSON, the report
// struct), because the --json field and the verify verdict that consume it
// live in files this change does not own. They now always see nil, which is
// the honest answer. Retiring the type, the field and the verdict together
// belongs to whoever owns env_status next.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ledgerFreshness is the answer to "does this checkout hold the newest copy
// of the env's ledger".
type ledgerFreshness int

const (
	// ledgerFreshnessUnknown: no comparison could be made — not a git
	// checkout, no origin, no default branch fetched. Reported, never a
	// failure: a project outside git is a supported way to run forge.
	ledgerFreshnessUnknown ledgerFreshness = iota
	// ledgerCurrent: the working tree's log equals origin's.
	ledgerCurrent
	// ledgerBehind: origin's log extends this one. Origin knows promotions
	// this checkout does not, so the declaration verify reads is stale.
	ledgerBehind
	// ledgerAhead: this log extends origin's — the normal state between
	// recording a release locally and merging the ledger PR.
	ledgerAhead
	// ledgerDiverged: neither is a prefix of the other. Two writers
	// appended to different copies; neither copy is the ledger.
	ledgerDiverged
)

func (f ledgerFreshness) String() string {
	switch f {
	case ledgerCurrent:
		return "current"
	case ledgerBehind:
		return "behind"
	case ledgerAhead:
		return "ahead"
	case ledgerDiverged:
		return "diverged"
	default:
		return "unknown"
	}
}

// MarshalJSON emits the lowercase name, so --json carries the same word the
// text report prints.
func (f ledgerFreshness) MarshalJSON() ([]byte, error) {
	return []byte(`"` + f.String() + `"`), nil
}

// UnmarshalJSON reads the name back, REFUSING one it does not know — the
// house rule for every --json enum. Defaulting an unknown word to "current"
// would read a newer binary's "this ledger is stale" as "fine".
func (f *ledgerFreshness) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("ledger state must be a string: %w", err)
	}
	for _, s := range []ledgerFreshness{ledgerFreshnessUnknown, ledgerCurrent, ledgerBehind, ledgerAhead, ledgerDiverged} {
		if s.String() == name {
			*f = s
			return nil
		}
	}
	return fmt.Errorf("unknown ledger state %q (expected current, behind, ahead, diverged or unknown)", name)
}

// stale reports whether the declaration cannot be trusted: a verdict against
// it would be about a ledger that is not the ledger.
func (f ledgerFreshness) stale() bool { return f == ledgerBehind || f == ledgerDiverged }

// ledgerFreshnessReport is what verify says about the declaration's source.
type ledgerFreshnessReport struct {
	State ledgerFreshness `json:"state"`
	// Ref is the upstream compared against, e.g. "origin/main".
	Ref string `json:"ref,omitempty"`
	// LocalEntries and UpstreamEntries count promotions in each copy.
	// "Behind" is only ever as of this checkout's last fetch of Ref.
	LocalEntries    int `json:"local_entries"`
	UpstreamEntries int `json:"upstream_entries"`
	// Detail explains the state in one line, including why it is unknown.
	Detail string `json:"detail,omitempty"`
}

// ledgerFreshnessChecker is the optional half of a binding store that can say
// whether its copy of an env's ledger is the newest one. Declared here, at
// the consumer.
//
// NOTHING IMPLEMENTS IT ANY MORE, and that is the correct end state rather
// than an oversight. The question "is my copy of the ledger stale" only
// existed because the ledger was a file COMMITTED TO THE CHECKOUT, so a
// branch that had not pulled the latest `chore(ledger): record vX` commit
// held an older history. Both of today's stores make the question
// meaningless:
//
//   - the control plane IS the ledger, so there is no copy to be behind;
//   - the machine ledger lives outside every checkout, keyed by project, so
//     every worktree on this machine reads the same one file.
//
// So ledgerFreshnessOf (env_status_release.go) now always reports nil and
// staleLedgerError never fires, which is a true answer: no checkout can be
// behind a ledger it does not hold. The vocabulary is kept because its
// consumer and its --json field live in files this change does not own;
// removing the type, the report field and the verify verdict together is
// cleanup for the task that owns env_status.
type ledgerFreshnessChecker interface {
	LedgerFreshness(ctx context.Context, env string) ledgerFreshnessReport
}

// ledgerLines splits a jsonl log into its non-blank lines, ignoring
// line-ending differences a checkout's autocrlf may have introduced. Still
// used for reading the RETIRED in-checkout ledger when deciding whether it
// has been imported (ledger_unimported.go).
func ledgerLines(log string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
