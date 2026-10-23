package cli

// `forge gate record` / `forge gate list` — post-promote check evidence
// (control-plane docs/design/hosted-deploy-primitives.md §3.3).
//
// THE GAP THIS CLOSES. Gates could only ever attach AT promote time, but the
// most important evidence about a promotion arrives after it: did the rollout
// converge, did the smoke pass, did a human sign it off. The promotion row is
// append-only, so none of that had anywhere to go, and the result was an
// evidence trail that stopped at the moment of least information.
//
// `gate record` is where it goes. A new append-only child table server-side,
// one row per (promotion, check, run) — so a re-run appends rather than
// overwrites, and the history of a flaky check is itself visible.
//
// WHY IT IS A TOP-LEVEL VERB AND NOT `forge env gate`. A gate is attached to
// a PROMOTION, not to an environment: the env is only how you find the
// promotion ("the current one"). Hanging it under `env` would imply the
// evidence moves when the env does, which is the opposite of what an
// append-only trail means.
//
// EXIT CODES (§3.3, and note what is NOT here):
//
//	0  recorded, or already recorded (idempotent)
//	1  invalid gate
//	2  unreachable, or auth refused
//	3  --release names a release that is not the env's current promotion
//
// RECORDING A `failed` GATE EXITS 0. The recording succeeded; that is what
// this command does. The check's own step already failed the job, and making
// `gate record` fail too would mean a pipeline that records evidence is
// strictly more likely to go red than one that stays silent — which is a
// direct incentive not to record. Evidence must be free to report bad news.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// newGateCmd is `forge gate`.
func newGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Record and read check evidence against a promotion",
		Long: `Attach check results to a promotion, and read them back.

A gate is one check's result — lint, test, smoke, rollout, a manual
sign-off — recorded against the promotion it is evidence about. Gates are
EVIDENCE, NOT ENFORCEMENT: forge records what it is told and who told it,
and refuses nothing on a gate's status. What a recorded gate proves is
that this credential claimed this result at this time.

Two attachment points, because they answer different questions:

  forge env deploy … --gate lint.json     what had passed BEFORE the
                                          environment moved (frozen into
                                          the promotion entry)
  forge gate record prod --from wait.json what was learned AFTER (appended
                                          to an append-only child record)

"Was this known before the button was pressed?" is the first question
asked about a bad release, and merging the two would lose it.

Recording needs deploy:write, which is deliberately weaker than promote:
a CI test job should be able to report its result without being able to
move production.`,
	}
	cmd.AddCommand(newGateRecordCmd())
	cmd.AddCommand(newGateListCmd())
	// Without this, cobra accepts `forge gate <typo>` and exits 0 — a
	// pipeline step that records nothing and reports success.
	return cmdutil.StrictGroup(cmd)
}

// gateRecordOptions are the flags of `forge gate record`.
type gateRecordOptions struct {
	// Which promotion. --promotion names one exactly; --release asserts
	// which one is current; neither means "the env's current promotion".
	promotionID string
	releaseVer  string

	// Where the gate comes from: a document, or stated inline.
	from    string
	name    string
	status  string
	url     string
	summary string

	jsonOut bool
	run     runOptions
}

// gateBackend is where an env's gate evidence lives, resolved from the env's
// own declaration.
//
// EVERY FIELD IS WRITTEN BY resolveGateBackend AND ONLY THERE. That is the
// point of it being a separate struct: these are resolved facts, not options,
// so they cannot be set by a flag and a half-filled one is not a state any
// caller can hold.
type gateBackend struct {
	// hosted records which ledger answered. This is the only honest way
	// to ask whether post-promote evidence can be stored at all: a file
	// ledger assigns promotion ids too (newPromotionID), so an id's
	// presence says nothing about whether anything can be appended to it.
	hosted bool
	// store records and reads gates. nil for a file-backed env, which is
	// refused before it is ever dereferenced.
	store *gateStore
	// bindings reads the env's current promotion.
	bindings bindingStore
}

// resolveGateBackend resolves the env's gate backend.
//
// A PACKAGE-LEVEL FUNCTION VARIABLE, not a struct field, because it is the
// SEAM — the same pattern readHostedSmokeStatus and newHostedSecretWriter use
// in this package. The alternative, nilable store/bindings fields on the
// options struct that only tests ever write, makes those fields permanently
// zero in production: every branch reading them is then decoration, and the
// tests setting them are green on a shape production cannot produce. The
// repo's dead-code guard rejects exactly that, correctly.
var resolveGateBackend = func(ctx context.Context, env string) (gateBackend, error) {
	projectDir := projectDirForKCL()
	ledger, err := ledgerFor(ctx, projectDir, env)
	if err != nil {
		return gateBackend{}, err
	}
	backend := gateBackend{hosted: ledger.Hosted, bindings: ledger.Bindings}
	if !backend.hosted {
		// Resolving a gate store would mean resolving a control plane
		// this env does not declare. Refuse with the reason instead of
		// with a credential error about an endpoint that is not there.
		return backend, errGateNeedsHostedLedger(env, ledger.Bindings.Location())
	}
	store, err := gateStoreForEnv(ctx, projectDir, env)
	if err != nil {
		return backend, err
	}
	backend.store = store
	return backend, nil
}

func newGateRecordCmd() *cobra.Command {
	var opts gateRecordOptions
	cmd := &cobra.Command{
		Use:   "record <environment>",
		Short: "Record one check's result against a promotion",
		Long: `Append one check result to a promotion.

The gate comes from a document, or is stated inline:

  forge gate record prod --from wait.json
  forge gate record prod --from smoke.json
  forge gate record prod --name qa-signoff --status passed --summary "checked by sean"

--from reads a gate-shaped JSON document (what --gate-json writes) OR any
forge --json document, deriving the check's name, verdict and summary from
it. So every forge verb that can judge something is recordable with no glue
script:

  forge env status prod --wait --json  > wait.json   && forge gate record prod --from wait.json
  forge env smoke prod --json > smoke.json  && forge gate record prod --from smoke.json
  forge lint --gate-json lint.json          && forge gate record prod --from lint.json

By default the gate attaches to the environment's CURRENT promotion. Name a
specific one with --promotion, or assert which release you believe is current
with --release: if the environment has moved on, that exits 3 rather than
recording evidence against the wrong promotion.

APPEND-ONLY AND IDEMPOTENT. A re-run appends a new row; a repeat of the same
(promotion, check, run id) returns the existing one. The run id includes the
CI attempt number, so a re-run of one step records afresh rather than
colliding with the first attempt.

RECORDING A FAILED GATE EXITS 0 — the recording succeeded. The check's own
step already failed the job.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGateRecord(cmd.Context(), args[0], opts, cmd.OutOrStdout())
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.promotionID, "promotion", "",
		"Promotion to attach the gate to (default: the environment's current promotion)")
	flags.StringVar(&opts.releaseVer, "release", "",
		"Assert the environment's current promotion is this release; exits 3 if it has moved on")
	flags.StringVar(&opts.from, "from", "",
		"Read the gate from a JSON document: a gate document (--gate-json output), or any forge --json document")
	flags.StringVar(&opts.name, "name", "",
		"Check name: lint, test, smoke, rollout, qa-signoff. Required unless --from names it")
	flags.StringVar(&opts.status, "status", "",
		"Verdict: passed | failed | skipped | error. Required unless --from carries one")
	flags.StringVar(&opts.url, "url", "", "Where the full report lives (a CI run, an artifact)")
	flags.StringVar(&opts.summary, "summary", "", "One line: \"412 passed, 0 failed, 3 skipped\"")
	flags.BoolVar(&opts.jsonOut, "json", false, "Emit one JSON document instead of the table")
	registerRunFlags(flags, &opts.run)
	return cmd
}

// gateRecordReport is `forge gate record --json`.
type gateRecordReport struct {
	jsonEnvelope
	Env         string `json:"env"`
	PromotionID string `json:"promotion_id,omitempty"`
	Release     string `json:"release,omitempty"`
	// Created is false when this exact (promotion, check, run) was already
	// recorded. Both are exit 0; they are different facts.
	Created bool          `json:"created"`
	Gate    *release.Gate `json:"gate,omitempty"`
}

func runGateRecord(ctx context.Context, env string, opts gateRecordOptions, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	report := gateRecordReport{Env: env}
	err := recordGateInto(ctx, env, opts, &report)
	if opts.jsonOut {
		report.stamp(err)
		if emitErr := emitJSONDocument(report); emitErr != nil {
			return emitErr
		}
		return err
	}
	if err != nil {
		return err
	}
	verb := "recorded"
	if !report.Created {
		verb = "already recorded"
	}
	fmt.Fprintf(out, "forge gate record %s\n", env)
	fmt.Fprintf(out, "  promotion %s%s\n", report.PromotionID, releaseSuffix(report.Release))
	fmt.Fprintf(out, "  %s:\n", verb)
	fmt.Fprint(out, renderGateTable([]release.Gate{*report.Gate}))
	return nil
}

func releaseSuffix(version string) string {
	if version == "" {
		return ""
	}
	return " (release " + version + ")"
}

// recordGateInto does the work and fills the report. Split from runGateRecord
// so the JSON envelope is stamped from the SAME error the text path returns —
// §3.A's rule that the two modes cannot disagree about the verdict.
func recordGateInto(ctx context.Context, env string, opts gateRecordOptions, report *gateRecordReport) error {
	gate, err := gateFromRecordOptions(opts)
	if err != nil {
		// An invalid gate is exit 1: we looked at it and it was wrong.
		return &gateExitError{code: exitWrong, msg: err.Error(), cause: err}
	}

	// The run id ties this check to the pipeline attempt that ran it, and
	// is the third element of the server's idempotency key. Resolved
	// BEFORE the network call so a contradictory --no-run/--run-id pair
	// fails locally.
	run, err := opts.run.resolveRun()
	if err != nil {
		return &gateExitError{code: exitWrong, msg: err.Error(), cause: err}
	}
	if gate.RunID == "" {
		gate.RunID = run.ID
	}
	if gate.URL == "" {
		gate.URL = run.URL
	}

	backend, err := resolveGateBackend(ctx, env)
	if err != nil {
		return classifyGateError(err, "record gate")
	}

	promotionID, version, err := resolveGatePromotion(ctx, env, opts.promotionID, opts.releaseVer, backend)
	if err != nil {
		return err
	}
	report.PromotionID, report.Release = promotionID, version

	recorded, created, err := backend.store.recordGate(ctx, promotionID, gate)
	if err != nil {
		return classifyGateError(err, "record gate")
	}
	report.Gate, report.Created = &recorded, created
	return nil
}

// gateFromRecordOptions builds the gate from --from and/or the inline flags.
//
// --from AND the inline flags COMPOSE rather than conflict: the document
// supplies the gate, and an explicit --url or --summary overrides what the
// document said. That is the case CI actually has — a verb's document knows
// the counts, and the workflow knows the URL of the run that produced it.
// --status is the exception; see below.
func gateFromRecordOptions(opts gateRecordOptions) (release.Gate, error) {
	if opts.from == "" {
		if opts.name == "" || opts.status == "" {
			return release.Gate{}, errors.New(
				"state the gate: --from <file.json>, or --name <check> --status passed|failed|skipped|error")
		}
		status, err := release.ParseGateStatus(opts.status)
		if err != nil {
			return release.Gate{}, err
		}
		gate := release.Gate{
			Name: opts.name, Status: status,
			URL: opts.url, Summary: opts.summary,
		}
		if err := gate.Validate(); err != nil {
			return release.Gate{}, err
		}
		return gate, nil
	}

	// --status BESIDE --from IS REFUSED. The document carries a verdict
	// the check itself reported; overriding it is overriding the evidence
	// with an opinion, which is the one thing a gate must not be able to
	// do quietly. (A name, url or summary is metadata ABOUT the check; the
	// status IS the check's answer.)
	if opts.status != "" {
		return release.Gate{}, errors.New(
			"--status cannot be combined with --from: the document carries the check's own verdict, " +
				"and overriding it would record an opinion as evidence")
	}
	gate, err := gateFromDocumentFile(opts.from, opts.name)
	if err != nil {
		return release.Gate{}, err
	}
	// AN EXPLICIT --name WINS over the shape's derived one.
	//
	// Inside gateFromDocument the name is only a HINT, because there a
	// document that names itself knows better than a default. Here it was
	// TYPED: the caller is distinguishing two records of the same check,
	// which is the case CI actually has — two `env wait` documents for two
	// environments both derive the name "wait", and the server's
	// idempotency key is (promotion, name, run id), so under one run id
	// the second would return the FIRST one's row instead of recording.
	// Letting the shape win would make that collision unavoidable.
	if opts.name != "" {
		gate.Name = opts.name
	}
	if opts.url != "" {
		gate.URL = opts.url
	}
	if opts.summary != "" {
		gate.Summary = opts.summary
	}
	if err := gate.Validate(); err != nil {
		return release.Gate{}, err
	}
	return gate, nil
}

// resolveGatePromotion decides WHICH promotion the gate attaches to.
//
// --promotion wins (it names one exactly). Otherwise the env's current
// promotion is read, and --release, when given, is an ASSERTION about it:
// a mismatch is exit 3, never a silent record against whatever is current
// now. That is the same compare-before-write discipline promote applies, for
// the same reason — evidence filed against the wrong promotion is worse than
// no evidence, because it reads as a vouched-for release.
func resolveGatePromotion(ctx context.Context, env, promotionID, releaseVer string, backend gateBackend) (string, string, error) {
	// A FILE-BACKED env is refused before anything else, including before
	// --promotion: there is no child record to append to whichever
	// promotion is named, so an id would only make the failure arrive
	// later and less clearly.
	if !backend.hosted {
		return "", "", errGateNeedsHostedLedger(env, backend.bindings.Location())
	}
	if promotionID != "" {
		return promotionID, "", nil
	}
	current, ok, err := backend.bindings.Current(ctx, env)
	if err != nil {
		return "", "", classifyGateError(err, "read the current promotion")
	}
	if !ok {
		return "", "", &gateExitError{
			code: exitWrong,
			msg:  fmt.Sprintf("environment %q has never been promoted, so there is no promotion to attach evidence to", env),
			hint: "promote a release first, or name the promotion with --promotion",
		}
	}
	if releaseVer != "" && releaseVer != current.Release {
		return "", "", &gateExitError{
			code: exitConflict,
			msg: fmt.Sprintf("%s currently runs %s, not %s — the environment moved on",
				env, current.Release, releaseVer),
			hint: "evidence recorded against the wrong promotion reads as a vouched-for release. " +
				"Re-read the current promotion, or name the one you mean with --promotion",
		}
	}
	return current.ID, current.Release, nil
}

// errGateNeedsHostedLedger is the answer for a file-backed env.
//
// A file ledger is append-only LINES: there is nowhere to append a child row
// to a line already written (release.Promotion.RecordedGates says exactly
// this). So post-promote evidence needs a control plane, and the message says
// so rather than failing obscurely — a user on the file backend has not done
// anything wrong, they have asked for something the backend cannot hold.
func errGateNeedsHostedLedger(env, location string) error {
	return &gateExitError{
		code: exitWrong,
		msg: fmt.Sprintf("environment %q keeps its ledger in files (%s), which cannot hold post-promote evidence",
			env, location),
		hint: "a file ledger's entries are lines already written, with nowhere to append to. " +
			"Attach the evidence at deploy time instead: `forge env deploy " + env + " <version> --gate <file.json>`",
	}
}

// resolve builds the gate store and the binding store, from the seams a test
// supplied or from the env's own declaration.
// gateStoreForEnv builds a gate store from the env's declared control plane —
// the same endpoint and credential precedence every other hosted verb uses,
// so a gate is recorded where the env's releases live and nowhere else.
func gateStoreForEnv(ctx context.Context, projectDir, env string) (*gateStore, error) {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil, fmt.Errorf("gate evidence for env %q: render deploy/kcl/%s: %w", env, env, err)
	}
	decl := declarationFromEntities(entities)
	if decl == nil {
		return nil, errGateNeedsHostedLedger(env, "deploy/kcl/"+env)
	}
	ep, err := cloud.ResolveEndpoint(env, decl)
	if err != nil {
		return nil, err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return nil, fmt.Errorf("gate evidence for env %q is kept on the control plane at %s: %w", env, ep.URL, err)
	}
	return &gateStore{client: cloud.NewClient(ep, cred), endpoint: ep.URL}, nil
}

// ─── gate list ──────────────────────────────────────────────────────────────

type gateListOptions struct {
	promotionID string
	jsonOut     bool
}

func newGateListCmd() *cobra.Command {
	var opts gateListOptions
	cmd := &cobra.Command{
		Use:   "list <environment>",
		Short: "Read every check result attached to a promotion",
		Long: `Print the evidence trail of a promotion.

Promote-time gates come first — what had already passed when the
environment was bound — then the gates recorded afterwards, oldest first.
That order carries the distinction a reader of a bad release needs first:
what was known BEFORE the environment moved, versus what was learned after.

Defaults to the environment's current promotion.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGateList(cmd.Context(), args[0], opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.promotionID, "promotion", "",
		"Promotion to read (default: the environment's current promotion)")
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "Emit one JSON document instead of the table")
	return cmd
}

// gateListReport is `forge gate list --json`.
type gateListReport struct {
	jsonEnvelope
	Env         string `json:"env"`
	PromotionID string `json:"promotion_id,omitempty"`
	Release     string `json:"release,omitempty"`
	// Gates is always non-nil so a consumer sees `[]`, never `null`.
	Gates []release.Gate `json:"gates"`
}

func runGateList(ctx context.Context, env string, opts gateListOptions, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	report := gateListReport{Env: env, Gates: []release.Gate{}}
	err := listGatesInto(ctx, env, opts, &report)
	if opts.jsonOut {
		report.stamp(err)
		if emitErr := emitJSONDocument(report); emitErr != nil {
			return emitErr
		}
		return err
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "forge gate list %s\n", env)
	fmt.Fprintf(out, "  promotion %s%s\n", report.PromotionID, releaseSuffix(report.Release))
	fmt.Fprint(out, renderGateTable(report.Gates))
	return nil
}

func listGatesInto(ctx context.Context, env string, opts gateListOptions, report *gateListReport) error {
	backend, err := resolveGateBackend(ctx, env)
	if err != nil {
		return classifyGateError(err, "list gates")
	}
	promotionID, version, err := resolveGatePromotion(ctx, env, opts.promotionID, "", backend)
	if err != nil {
		return err
	}
	report.PromotionID, report.Release = promotionID, version

	gates, err := backend.store.listGates(ctx, promotionID)
	if err != nil {
		return classifyGateError(err, "list gates")
	}
	if gates != nil {
		report.Gates = gates
	}
	return nil
}
