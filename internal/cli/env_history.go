package cli

// `forge env status <env> --history`: an environment's promotion ledger, newest first
// (hosted-deploy-primitives §3.5, task F7).
//
// What has <env> run, by whom, with what evidence? Each row is one ledger
// entry: release, where it was promoted from, who, the note, both halves of
// its evidence (gates claimed at promote time, gates recorded afterwards),
// whether it superseded an unfinished rollout, and the CI run that wrote it.
//
// Exit codes: 0 the ledger was read (an empty history is a successful read),
// 2 it could not be. Nothing here judges a release, so there is no 1.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/reliant-labs/forge/pkg/release"
)

// envHistoryDocument is the --json output.
type envHistoryDocument struct {
	jsonEnvelope
	Env    string `json:"env"`
	Ledger string `json:"ledger"`
	// Promotions are newest first. Always non-nil.
	Promotions []envHistoryEntry `json:"promotions"`
	// NextBefore is the --before for the next page; empty on the last.
	NextBefore string `json:"next_before"`
}

// envHistoryEntry is one promotion as history renders it: the ledger entry
// plus its evidence count, so a reader can badge it without walking gates.
type envHistoryEntry struct {
	release.Promotion
	Gates *gatesSummary `json:"gates_summary,omitempty"`
}

func runEnvHistory(ctx context.Context, store bindingStore, env string, q historyQuery, jsonOut bool, out io.Writer) error {
	reader, ok := store.(bindingHistoryReader)
	if !ok {
		return exitCodeError{code: exitUndetermined, msg: fmt.Sprintf("the ledger at %s cannot list history", store.Location())}
	}
	page, err := reader.HistoryPage(ctx, env, q)
	if err != nil {
		code := exitUndetermined
		// A bad --limit or an unknown cursor is the REQUEST, not the
		// ledger: re-reading cannot fix it, so it is not "could not look".
		if errors.Is(err, errHistoryQueryInvalid) {
			code = exitWrong
		}
		return exitCodeError{code: code, msg: fmt.Sprintf("read the promotion history of %s (%s): %v", env, store.Location(), err)}
	}

	doc := envHistoryDocument{Env: env, Ledger: store.Location(), NextBefore: page.Next, Promotions: []envHistoryEntry{}}
	for _, p := range page.Promotions {
		doc.Promotions = append(doc.Promotions, envHistoryEntry{Promotion: p, Gates: summarizeGates(p.Gates, p.RecordedGates)})
	}
	doc.stamp(nil)
	if jsonOut {
		return emitJSONDocument(doc)
	}
	renderEnvHistory(out, doc)
	return nil
}

func renderEnvHistory(out io.Writer, doc envHistoryDocument) {
	fmt.Fprintf(out, "Promotion history of %s  (%s)\n\n", doc.Env, doc.Ledger)
	if len(doc.Promotions) == 0 {
		fmt.Fprintln(out, "  no promotions — this environment has never been promoted (or none match the filter)")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  PROMOTED\tRELEASE\tFROM\tBY\tEVIDENCE\tID")
	for _, e := range doc.Promotions {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			formatLedgerTime(e.PromotedAt), e.Release, emptyDash(e.FromEnv), actorLabel(e.PromotedBy), evidenceLabel(e), e.ID)
		if e.Note != "" {
			fmt.Fprintf(tw, "  \t  note: %s\t\t\t\t\n", e.Note)
		}
	}
	_ = tw.Flush()
	if doc.NextBefore != "" {
		fmt.Fprintf(out, "\n  more: forge env status %s --history --before %s\n", doc.Env, doc.NextBefore)
	}
}

func evidenceLabel(e envHistoryEntry) string {
	var parts []string
	// First, because it changes what the row means: this release was
	// recorded but never reached the target.
	if g, ok := latestApplyGate(e.Promotion); ok && g.Status == release.GateStatusFailed {
		parts = append(parts, "NOT APPLIED")
	}
	if s := e.Gates; s != nil {
		parts = append(parts, fmt.Sprintf("%d passed", s.Passed))
		if s.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d FAILED", s.Failed))
		}
		if s.Errored > 0 {
			parts = append(parts, fmt.Sprintf("%d errored", s.Errored))
		}
		if s.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped", s.Skipped))
		}
	}
	if e.SupersededInFlight {
		parts = append(parts, "SUPERSEDED IN FLIGHT")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func actorLabel(a release.Actor) string {
	switch {
	case a.User != "" && a.Actor != "":
		return a.User + " via " + a.Actor
	case a.User != "":
		return a.User
	case a.Actor != "":
		return a.Actor
	default:
		return "-"
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
