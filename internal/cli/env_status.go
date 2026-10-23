package cli

// `forge env status [environment...]` — THE one read view of an environment
// (ADR docs/adr/env-verbs.md, task V4).
//
// WHY ONE COMMAND. Reading an environment used to take six verbs — `env
// status`, `env verify`, `env wait`, `env rollout`, `env topology`, `env
// history` — and they were views of a single question, split by which half of
// the answer each happened to own. A reader had to know, before they could
// ask, that the bound release lived in `verify`, the rollout phase in
// `rollout`, the runtime ports in `status`, and the promotion that caused all
// of it in `history`. Worse, two of the six were literal duplicates:
// `env rollout` WAS `env wait --timeout 0`.
//
// So they are modes of one command now, and the modes are the questions a
// reader actually has:
//
//	forge env status              every environment (the old `env topology`)
//	forge env status prod         prod, right now: runtime AND release
//	forge env status prod --wait  block until the rollout settles (old `env wait`)
//	forge env status prod --history  what has prod run (old `env history`)
//
// EACH MODE REACHES THE SAME CODE THE DELETED VERB CALLED. Nothing was
// reimplemented in the move: --wait is runEnvWait, --history is runEnvHistory,
// the all-envs view is runEnvTopology, and the release half is the old
// verify's runEnvStatusRelease. That is what makes "every exit code is
// unchanged" a property of the wiring rather than a promise.
//
// EXIT CODES COME FROM THE HALF THAT OWNS THEM, and the one subtlety worth
// knowing is that the two halves of the default view are NOT symmetric:
//
//   - The RUNTIME half reports and never fails. "The app is down" is a state
//     this command must be able to print — a status verb that exited non-zero
//     for it would be unusable as the one read view, and the runtime checks
//     have always had that contract.
//   - The RELEASE half's verdict IS the command's exit code (0 / 1 / 2),
//     because that half makes a CLAIM: the env is or is not running what it
//     declares. 1 is "we looked and it is wrong", 2 is "we could not look".
//
// --wait keeps 0/1/5/6/2 and --history keeps 0/1/2, both untouched.

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// envStatusLongHelp is `forge env status`'s help text.
//
// A const rather than an inline literal because the text runs to ninety lines
// and the constructor around it is otherwise a flag table: inline, a reader
// looking for where --wait is declared scrolls past the whole manual first,
// and each addition to the help pushed the constructor further over the
// length budget for reasons that had nothing to do with its logic.
const envStatusLongHelp = `Report everything about an environment in one read.

  forge env status                 every environment, and how far behind each is
  forge env status prod            prod right now: runtime AND release
  forge env status prod --wait     block until the rollout settles
  forge env status prod --history  prod's promotion ledger, newest first

WHAT ONE ENV'S REPORT CARRIES:

  * the BOUND RELEASE — the version this env's promotion ledger declares, and
    when it was promoted (promote time, NOT deploy time; that gap is what the
    verify half exists to measure);
  * VERIFY — the digests the env is actually RUNNING against the ones the
    binding froze, per declared image, in five states: match / drift /
    missing / untagged / unreachable. A hosted env is read through its
    control plane's observer, because forge cannot read a hosted cluster;
  * the ROLLOUT PHASE per workload, as the control plane computes it;
  * IDENTITY — a hosted env that declares forge.ManagedIdentity gets one line,
    "identity: ready / provisioning / failed: <reason>", as its control plane
    reports it. It never changes the exit code here; --wait (and env deploy)
    waits on it: a release whose rollout succeeded is WAITING ON IDENTITY until
    the identity is ready (1 if it failed, the timed-out code if it was still
    provisioning at the deadline);
  * RUNTIME HEALTH — every host service and frontend with its URL, log file
    and whether a listener is accepting right now, plus the compose infra,
    the app's /healthz + /readyz, pprof and the telemetry backends;
  * GATES — the evidence recorded for the current promotion;
  * LEDGER FRESHNESS — whether this checkout's copy of the promotion log is
    the newest one;
  * RECORDS — the PROVENANCE of the bound release (tree, commit, branch,
    dirty) and how the binding was made; the CONVERGENCE of it (what the
    reconciler did, onto which bundle, when it was observed); and the LOCAL
    SESSIONS running it.

CONVERGENCE IS AN OBSERVATION, NOT FORGE'S REPORT. forge never applies to a
cluster — a reconciler converges each environment to its promoted bundle — so
this is read from the environment's control plane and labelled as what the
control plane observed. An environment with no control plane has no reconciler
and shows one plain line saying so. If a control plane has not reported yet,
the report says that rather than inventing a state.

RECORDS DESCRIBE, THEY DO NOT JUDGE. An unreadable records store never
changes the exit code — the cluster comparison above already has an opinion
about whether the bytes landed. An environment with no records shows the empty
state plainly, which is an answer, never an error.

LOCAL SESSIONS are PRESENCE ONLY: one row per worktree per machine, recorded
best-effort by ` + "`forge env up`" + `, and never a promotion, a deploy target or an
input to policy or billing. A record nothing refreshes is discarded after 24h,
so a crashed stack simply goes quiet — shown as "quiet", not as failed, because
forge cannot tell a crash from a closed laptop. Only LOCAL environments have
sessions; for any other kind the report says so rather than showing an empty
list, since "no sessions" and "sessions do not apply here" are different facts.

THE TWO HALVES ARE NOT SYMMETRIC ABOUT FAILURE, on purpose. Runtime health
REPORTS: "the app is down" is a state this command must be able to print, so
it never changes the exit code. The release half makes a CLAIM — the env is or
is not running what it declares — so its verdict is the exit code.

` + "`--wait`" + ` blocks until the rollout of a promotion finishes, and reports whether
it STAYED up past the stability window. It is pinned to ONE promotion, never
to "whatever the env declares now", so a hotfix promoted mid-wait reports
SUPERSEDED rather than succeeding on bytes it was never asked about.
` + "`--wait --timeout 0`" + ` reads the phase ONCE and never blocks.

` + "`--history`" + ` pages the promotion ledger with a keyset cursor: pass the previous
page's ` + "`next_before`" + ` as --before, and stop on an EMPTY next_before rather than
on a short page.

EXIT CODES — a pipeline branches on these directly:

  0  everything declared is running (or nothing is declared)
  1  we looked and it is WRONG — an image drifted or is missing; with --wait,
     a workload is DEGRADED
  2  we could not DETERMINE — a cluster or control plane was unreachable, a
     credential was refused, the thing is unobservable, or this checkout's
     promotion ledger is stale
  6  --wait only: SUPERSEDED — a newer promotion replaced the one waited on
  7  QUEUED — the bound release is accepted and recorded, and the control plane
     holds it on a person (billing). The output names what it waits on and the
     action URL; it goes live by itself once they act. --wait waits through it
     (still queued at the deadline is 7 again)
  8  --wait only: TIMED OUT while still pending / progressing / stabilizing.
     The rollout was progressing, so retry the wait; do not re-promote

1 and 2 are separate because CI must tell a bad release from a broken control
plane; a gate that reports both with one code gets switched off the first week
it is wrong about one of them. 6, 7 and 8 are deliberately not 1: "the release
was overtaken", "a person has to act" and "we never saw this finish" are not
"the release is bad".

A STALE FILE LEDGER IS EXIT 2. .forge/promotions/<env>.jsonl is committed to
git, so a checkout that has not pulled compares the cluster against an OLDER
promotion — a fine deploy reads as DRIFT, and a deploy that never happened can
read as MATCH. Neither is evidence, so the verdict is "could not determine"
with the fix. AHEAD (a release recorded here, not yet merged) is noted, not
failed.

--json emits ONE document with the same verdict and IDENTICAL exit codes;
` + "`ok`" + ` is false exactly when the process exits non-zero.

Examples:
  forge env status                                  # the whole topology, offline
  forge env status prod                             # did prod receive its release?
  forge env status prod --wait                      # gate a release on the rollout
  forge env status prod --wait --timeout 0 --json   # where is it NOW? one read
  forge env status prod --history --limit 1 --json | jq -r '.promotions[0].id'
  forge env status prod --json | jq -r '.images[] | select(.state == "drift")'`

// newEnvStatusCmd is `forge env status [environment...]`.
//
// The env is OPTIONAL — that is the one arity change the merge makes, and it
// is what absorbs the old `env topology`: with no env there is nothing to
// scope to, so the answer is every env. Several envs are accepted for the
// same reason topology accepted them, to include one that is bound in the
// ledger but not declared in this checkout.
func newEnvStatusCmd() *cobra.Command {
	var (
		jsonOut bool
		signal  string
		verbose bool

		// timeout serves two modes with DIFFERENT budgets, which is
		// why its default is the zero sentinel rather than either
		// one: a cluster read gets 60s (an unreachable cluster must
		// fail a CI job, not hang it) and a wait gets 15m (a cold
		// cloud rollout legitimately takes minutes). One flag cannot
		// declare two defaults, so unset resolves per mode below.
		timeout time.Duration

		// --wait: the old `env wait`, whose options this fills.
		wait     bool
		waitOpts envWaitOptions

		// --history: the old `env history`.
		history bool
		histQ   historyQuery

		// The all-envs view's cluster reconciliation.
		verifyClusters bool
	)

	cmd := &cobra.Command{
		Use:   "status [environment...]",
		Short: "The one read view of an environment: bound release, rollout, health, verify, gates, ledger",
		Args:  cobra.ArbitraryArgs,
		Long:  envStatusLongHelp,
		// The command's findings ARE its output; a cobra usage dump on a
		// drift failure would bury them under the flag list.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatchEnvStatus(cmd, args, envStatusModes{
				wait: wait, history: history, verifyClusters: verifyClusters,
				jsonOut: jsonOut, signal: signal, verbose: verbose,
				timeout: timeout, waitOpts: waitOpts, histQ: histQ,
			})
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&jsonOut, "json", false, "Emit ONE machine-readable document (same exit codes as text mode)")

	// The runtime half.
	flags.StringVar(&signal, "signal", "", "Run only one runtime signal: app, metrics, traces, logs, profiles (default: all)")
	flags.BoolVarP(&verbose, "verbose", "v", false, "Show evidence for all runtime checks (not just failures)")

	// The release half, and the all-envs view's cluster read.
	// Default 0 is the UNSET sentinel, not a budget: the two modes resolve
	// it differently (60s for a cluster read, 15m for a wait) and one flag
	// cannot declare both. The help names both numbers so the default
	// shown as "0s" is not mistaken for "no timeout".
	flags.DurationVar(&timeout, "timeout", 0,
		fmt.Sprintf("Maximum time to spend reading the cluster (default %s). With --wait this is the whole wait budget instead (default %s); '--wait --timeout 0' reads the phase ONCE and never blocks",
			defaultEnvStatusReleaseTimeout, envWaitDefaultTimeout))
	flags.BoolVar(&verifyClusters, "verify", false,
		"All-environments view only: also read each environment's cluster and reconcile it against the ledger (slow, needs credentials)")

	// --wait: the retired `env wait`, every flag unchanged.
	flags.BoolVar(&wait, "wait", false, "Block until the rollout settles, and prove it stayed up (exit 0/1/2/5/6)")
	flags.StringVar(&waitOpts.PromotionID, "promotion", "",
		"Promotion id to read (default: the env's current promotion). A CI retry passes the id the deploy returned")
	flags.StringVar(&waitOpts.Release, "release", "",
		"Refuse unless the promotion read binds this release version (exit 6 if it moved on). With --history, only promotions of this version")
	cmd.MarkFlagsMutuallyExclusive("promotion", "release")
	flags.DurationVar(&waitOpts.StableFor, "stable-for", 0,
		"--wait only: extra hold AFTER the phase reaches succeeded (default 0: the server's own stability window already applies)")
	flags.BoolVar(&waitOpts.FailFast, "fail-fast", false,
		"--wait only: exit 1 on the FIRST degraded observation instead of waiting out --timeout")
	flags.BoolVar(&waitOpts.IncludeUnpinned, "include-unpinned", false,
		"Let a degraded UNPINNED workload (a database, a third-party image) count too")
	flags.DurationVar(&waitOpts.Interval, "interval", envWaitDefaultInterval, "--wait only: poll cadence")
	flags.BoolVar(&waitOpts.WatchJSON, "watch-json", false,
		"--wait only: emit NDJSON, one line per phase change, as the rollout progresses")

	// --history: the retired `env history`, every flag unchanged.
	flags.BoolVar(&history, "history", false, "Page the environment's promotion ledger, newest first")
	flags.IntVar(&histQ.Limit, "limit", defaultHistoryLimit, fmt.Sprintf("--history only: entries per page (1–%d)", maxHistoryLimit))
	flags.StringVar(&histQ.Before, "before", "",
		"--history only: return entries older than this promotion id (the previous page's next_before)")

	// --wait's budget and the cluster-read budget are the same flag with
	// two meanings, which only works because they are never both in play.
	cmd.MarkFlagsMutuallyExclusive("wait", "history")
	cmd.MarkFlagsMutuallyExclusive("wait", "verify")
	cmd.MarkFlagsMutuallyExclusive("history", "verify")

	return cmd
}

// envStatusModes is the resolved flag state one dispatch needs. A struct
// rather than eleven parameters because they are read TOGETHER to pick a
// mode, and a dispatcher that took them positionally would be impossible to
// call correctly at a glance.
type envStatusModes struct {
	wait, history, verifyClusters bool
	jsonOut, verbose              bool
	signal                        string
	timeout                       time.Duration
	waitOpts                      envWaitOptions
	histQ                         historyQuery
}

// dispatchEnvStatus picks the mode and runs it.
//
// Split out of the command's RunE so the mode CHOICE is one readable
// sequence — arity first, then the per-env modes — instead of being buried
// under two hundred lines of flag declarations and help text.
func dispatchEnvStatus(cmd *cobra.Command, args []string, m envStatusModes) error {
	perEnv := m.wait || m.history
	// --wait and --history are two different reads of one env, and asking
	// both at once has no answer. Refused rather than silently preferring
	// one. (cobra also marks them exclusive; this is the backstop for a
	// direct call.)
	if m.wait && m.history {
		return fmt.Errorf("--wait and --history are two different reads: --wait blocks on the CURRENT rollout, --history pages PAST promotions. Pass one")
	}
	// Not exactly one env: the all-environments view. There is nothing to
	// block on and no single ledger to page, so the per-env modes are
	// REFUSED rather than quietly ignored — silently dropping --wait would
	// return an instant snapshot to a caller gating a release on it.
	if len(args) != 1 {
		if perEnv {
			if len(args) == 0 {
				return fmt.Errorf("--wait and --history need an environment: `forge env status` with no environment is the all-environments view")
			}
			return fmt.Errorf("--wait and --history read ONE environment, got %d", len(args))
		}
		return runEnvTopology(cmd.Context(), args, envTopologyOptions{
			JSON:       m.jsonOut,
			Verify:     m.verifyClusters,
			Timeout:    m.timeout,
			ProjectDir: projectDirForKCL(),
			Lister:     kubectlImageLister{},
			Resolver:   kclTargetResolver{},
		})
	}

	env := args[0]
	switch {
	case m.wait:
		m.waitOpts.Timeout, m.waitOpts.Once = resolveWaitBudget(cmd, m.timeout)
		m.waitOpts.JSON = m.jsonOut
		return runEnvWaitForCmd(cmd.Context(), env, m.waitOpts)
	case m.history:
		// --release means the same thing in both per-env modes ("this
		// version"), so it is ONE flag — and it must be relayed to
		// whichever mode is running. Reading it off the shared
		// destination rather than declaring a second --release is what
		// keeps `--wait --release` and `--history --release` from
		// drifting apart.
		m.histQ.Release = m.waitOpts.Release
		store, err := bindingStoreFor(cmd.Context(), projectDirForKCL(), env)
		if err != nil {
			return exitCodeError{code: exitUndetermined, msg: err.Error()}
		}
		return runEnvHistory(cmd.Context(), store, env, m.histQ, m.jsonOut, cmd.OutOrStdout())
	}
	return runEnvStatus(cmd.Context(), env, envStatusOptions{
		Timeout:  m.timeout,
		JSON:     m.jsonOut,
		Signal:   m.signal,
		Verbose:  m.verbose,
		Lister:   kubectlImageLister{},
		Resolver: kclTargetResolver{},
	})
}

// resolveWaitBudget turns --timeout into a wait budget, or into the
// single-read mode.
//
// `--timeout 0` means ONE READ, never block — the snapshot the retired
// `env rollout` was. An UNSET --timeout keeps the 15m budget.
//
// Those two have to be told apart, and a zero value cannot do it: Timeout's
// zero IS the unset value, so "0" and "not given" arrive identically in the
// struct. cobra's Changed is the only thing that knows the difference, and it
// is only available at the command layer, which is why this takes the command
// rather than a duration.
//
// Reading it wrong in either direction is bad in its own way. An unset flag
// becoming a single read would turn every plain --wait into a non-blocking
// poll that reports 5 the moment a rollout is mid-flight. An explicit 0
// becoming 15m would make the snapshot block for a quarter of an hour.
//
// The 15m default also cannot live on the flag itself: the same --timeout
// bounds the release half's cluster read, where the right budget is 60s. One
// flag cannot declare two defaults, so the flag's default is the unset
// sentinel and each mode resolves it.
func resolveWaitBudget(cmd *cobra.Command, timeout time.Duration) (budget time.Duration, once bool) {
	switch {
	case !cmd.Flags().Changed("timeout"):
		return envWaitDefaultTimeout, false
	case timeout == 0:
		return 0, true
	default:
		return timeout, false
	}
}

// runEnvStatus is the DEFAULT view of one env: runtime first, then release.
//
// Runtime comes first because it is the answer most invocations want and it
// is the cheap half — no cluster credentials, no ledger. The release half
// reads last and owns the verdict.
//
// The two halves are deliberately sequential rather than concurrent. They
// print one after the other and a reader follows them in order; running them
// in parallel would interleave two progress streams for no gain on a command
// whose slow part is one cluster read.
func runEnvStatus(ctx context.Context, env string, opts envStatusOptions) error {
	// The runtime half REPORTS. Its error is returned only for a usage
	// mistake (a mistyped --signal), never for a down stack — see the file
	// header on why the two halves are asymmetric about failure.
	if opts.JSON {
		// COLLECTED, not printed: the release half emits the single
		// document, with this nested inside it. Printing here would put
		// two documents on stdout and produce a stream no `jq`
		// invocation can read.
		runtime, err := collectRuntimeStatus(ctx, env, opts.Signal, opts.Verbose)
		if err != nil {
			return err
		}
		opts.Runtime = &runtime
		return runEnvStatusRelease(ctx, env, opts)
	}
	if err := renderRuntimeStatus(ctx, env, opts.Signal, opts.Verbose); err != nil {
		return err
	}
	fmt.Println()
	// The release half owns the exit code.
	return runEnvStatusRelease(ctx, env, opts)
}
