package cli

// `forge ledger show [<env>] --json` — the machine ledger's records, for the
// daemon.
//
// THIS IS THE ONE DELIBERATE EXCEPTION TO "LIVE NEVER NEEDS THE DAEMON"
// (doc §7.4, and O-14 names it as such). For an env that declares a control
// plane, Live reads hosted rows and the daemon is never involved. For an env
// with NO control plane there are no hosted rows to read — its promotions,
// applies and local sessions exist only in one machine's ledger — so the only
// way the UI can show them is to ask the daemon on that machine, through this
// command.
//
// That is unavoidable rather than an oversight, and the honest consequence is
// what the exception costs: this view is daemon-dependent, and the UI says so
// rather than rendering an empty list. "No sessions" and "cannot see
// sessions" are different facts, and a surface that collapses them tells a
// user their dev stack is down when really their daemon is.
//
// WHICH IS WHY EVERY LIST HERE IS NON-NULL IN --json. An absent key and an
// empty array would read the same to a consumer that forgot to distinguish
// them, so the document always carries the arrays; the only thing that can be
// missing is the whole document, which means the daemon could not be reached.

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// ledgerShowDoc is what the daemon returns for one project's machine ledger.
type ledgerShowDoc struct {
	// Project and Location say WHICH ledger answered. A reader comparing
	// two machines' views needs both, and "the ledger was empty" is a
	// different diagnosis from "I read a different ledger than I meant
	// to".
	Project  string `json:"project"`
	Location string `json:"location"`
	// Env is the environment filter, or "" for every env.
	Env string `json:"env,omitempty"`
	// Envs are the ledger's env records.
	Envs []release.EnvRecord `json:"envs"`
	// Environments carries the per-env records, newest promotion first
	// within each.
	Environments []ledgerShowEnv `json:"environments"`
	// Sessions are the local stacks running on THIS machine (§7.4's
	// presence rows). They are observation, never a promotion and never a
	// deploy target (O-8).
	Sessions []ledgerShowSession `json:"sessions"`
}

// ledgerShowEnv is one env's records.
type ledgerShowEnv struct {
	Name string `json:"name"`
	// Current is the env's binding: its newest promotion, or nil when it
	// has never been promoted. Nil rather than omitted, because "never
	// promoted" is a normal state a reader must be able to render.
	Current *release.Promotion `json:"current"`
	// Promotions are NEWEST FIRST, the order every other forge surface
	// reads a history in.
	Promotions []release.Promotion `json:"promotions"`
	// Applies are newest first, each joined to its outcome. A nil outcome
	// past its deadline reads as abandoned, which is deliberately
	// distinct from failed (F-2).
	Applies []ledgerShowApply `json:"applies"`
}

// ledgerShowApply is one apply with its outcome and DERIVED state.
//
// The state is computed here rather than left to the consumer because
// release.DeriveApplyState is the one implementation of that rule, and a UI
// that re-derived "is this abandoned" from a deadline and a clock would
// eventually disagree with what forge reports in a terminal.
type ledgerShowApply struct {
	Apply   release.Apply         `json:"apply"`
	Outcome *release.ApplyOutcome `json:"outcome,omitempty"`
	State   release.ApplyState    `json:"state"`
}

// ledgerShowSession is one presence row plus the two verdicts a renderer
// needs.
type ledgerShowSession struct {
	Session release.LocalSession `json:"session"`
	// Live means not stopped; Stale means live but unseen for longer than
	// release.SessionStaleAfter. Both are derived from the same clock
	// reading, so a row cannot come back live-and-not-live.
	Live  bool `json:"live"`
	Stale bool `json:"stale"`
}

// newLedgerShowCmd is `forge ledger show`.
func newLedgerShowCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "show [environment]",
		Short: "Print this machine's ledger records — promotions, applies and local sessions",
		Args:  cobra.MaximumNArgs(1),
		Long: `Read the records in this machine's ledger: each environment's promotions and
applies, and the local stacks running here.

WHY THIS EXISTS. An environment that declares ` + "`forge.ControlPlane`" + ` has hosted
rows, and a UI reads them directly. An environment with no control plane does
not: its history and its running stacks exist only in one machine's ledger
under $FORGE_LEDGER_HOME. So this is how that half becomes visible — the
reliant daemon runs it and the UI renders the result.

It is therefore a DAEMON-DEPENDENT view, and the only one. A surface showing
it must distinguish "no sessions" from "cannot see sessions": they are
different facts, and collapsing them tells a user their dev stack is down when
really their daemon is.

Local sessions are PRESENCE ONLY. A session is an observation — never a
promotion, never a deploy target — and nothing reads it for policy or billing.

Examples:
  ` + Name() + ` ledger show                  # every environment
  ` + Name() + ` ledger show dev --json       # one environment, for the daemon`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			env := ""
			if len(args) == 1 {
				env = args[0]
			}
			store, err := openMachineLedger(projectDirForKCL())
			if err != nil {
				return err
			}
			doc, err := ledgerShow(store, hostedProjectName(), env, time.Now())
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			writeLedgerShow(cmd.OutOrStdout(), doc, time.Now())
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the records as JSON (the form the reliant daemon's hook returns)")
	return cmd
}

// ledgerShow reads the records.
//
// now is a parameter rather than read from the clock inside, so the derived
// verdicts (stale, abandoned) are computed from ONE instant across every
// record. Reading time.Now() per row would let two sessions of one stack
// disagree about whether they are stale, and would make the output
// untestable.
func ledgerShow(store *ledgerfile.Store, project, env string, now time.Time) (ledgerShowDoc, error) {
	doc := ledgerShowDoc{
		Project:  project,
		Location: store.Dir(),
		Env:      env,
		// Non-null by construction, for the reason in the file header.
		Envs:         []release.EnvRecord{},
		Environments: []ledgerShowEnv{},
		Sessions:     []ledgerShowSession{},
	}
	envs, err := store.Envs()
	if err != nil {
		return doc, err
	}
	for _, rec := range envs {
		if env != "" && rec.Name != env {
			continue
		}
		doc.Envs = append(doc.Envs, rec)
	}

	for _, name := range ledgerShowEnvNames(store, envs, env) {
		shown, err := ledgerShowOneEnv(store, name, now)
		if err != nil {
			return doc, err
		}
		doc.Environments = append(doc.Environments, shown)
	}

	sessions, err := store.Sessions()
	if err != nil {
		return doc, err
	}
	for _, sess := range sessions {
		if env != "" && sess.Env != env {
			continue
		}
		doc.Sessions = append(doc.Sessions, ledgerShowSession{
			Session: sess,
			Live:    sess.Live(),
			Stale:   sess.Stale(now),
		})
	}
	sort.SliceStable(doc.Sessions, func(i, j int) bool {
		a, b := doc.Sessions[i].Session, doc.Sessions[j].Session
		if a.Env != b.Env {
			return a.Env < b.Env
		}
		return a.Worktree.Key < b.Worktree.Key
	})
	return doc, nil
}

// ledgerShowOneEnv reads one env's promotions and applies, newest first.
func ledgerShowOneEnv(store *ledgerfile.Store, name string, now time.Time) (ledgerShowEnv, error) {
	shown := ledgerShowEnv{
		Name:       name,
		Promotions: []release.Promotion{},
		Applies:    []ledgerShowApply{},
	}
	promotions, err := store.Promotions(name)
	if err != nil {
		return shown, err
	}
	if len(promotions) > 0 {
		// The CURRENT binding is the last line, which is the ledger's
		// one rule — taken before the reversal, so a change to the
		// display order cannot silently change which promotion is
		// reported as current.
		current := promotions[len(promotions)-1]
		shown.Current = &current
	}
	for i := len(promotions) - 1; i >= 0; i-- {
		shown.Promotions = append(shown.Promotions, promotions[i])
	}

	applies, err := store.Applies(name)
	if err != nil {
		return shown, err
	}
	for i := len(applies) - 1; i >= 0; i-- {
		a := applies[i]
		shown.Applies = append(shown.Applies, ledgerShowApply{
			Apply:   a.Apply,
			Outcome: a.Outcome,
			State:   release.DeriveApplyState(a.Apply, a.Outcome, now),
		})
	}
	return shown, nil
}

// ledgerShowEnvNames is every env to report on: the declared ones plus any
// with a promotion log, filtered to one when asked.
//
// An env named on the command line is reported even when the ledger holds
// NOTHING for it, with empty lists. That is the empty-versus-unknown rule
// again: "dev has no promotions here" is an answer, and returning no
// environments at all would read as "I do not know about dev".
func ledgerShowEnvNames(store *ledgerfile.Store, envs []release.EnvRecord, only string) []string {
	if only != "" {
		return []string{only}
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range envs {
		if !seen[e.Name] {
			seen[e.Name] = true
			out = append(out, e.Name)
		}
	}
	for _, name := range promotionLogEnvs(store) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// writeLedgerShow is the human form.
func writeLedgerShow(w io.Writer, doc ledgerShowDoc, now time.Time) {
	fmt.Fprintf(w, "ledger %s (project %s)\n", doc.Location, emptyAs(doc.Project, "(unnamed)"))
	if len(doc.Environments) == 0 {
		fmt.Fprintf(w, "  no environments recorded\n")
	}
	for _, env := range doc.Environments {
		fmt.Fprintf(w, "\n%s\n", env.Name)
		if env.Current == nil {
			fmt.Fprintf(w, "  never promoted\n")
		} else {
			fmt.Fprintf(w, "  runs %s (promoted %s)\n",
				env.Current.Release, env.Current.PromotedAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(w, "  promotions: %d\n", len(env.Promotions))
		for _, a := range env.Applies {
			fmt.Fprintf(w, "  apply %s: %s\n", a.Apply.ID, a.State)
		}
	}
	if len(doc.Sessions) == 0 {
		fmt.Fprintf(w, "\nno local stacks recorded here\n")
		return
	}
	fmt.Fprintf(w, "\nlocal stacks on this machine:\n")
	for _, s := range doc.Sessions {
		state := "stopped"
		switch {
		case s.Stale:
			// Named by WHEN it was last seen rather than as a
			// failure: a stale session may be a stack that is fine
			// and a daemon that stopped heartbeating, and forge
			// cannot tell which.
			state = "last seen " + s.Session.LastSeenAt.UTC().Format("15:04:05")
		case s.Live:
			state = "running"
		}
		label := s.Session.Worktree.Label
		if label == "" {
			label = "(primary checkout)"
		}
		fmt.Fprintf(w, "  %s on %s [%s]: %s\n", s.Session.Env, s.Session.Worktree.Host, label, state)
	}
	_ = now
}
