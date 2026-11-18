package cli

// `forge ledger` — the verbs that act on a ledger as a whole, independent of
// any one environment.
//
// `forge release` is the noun for a release LEDGER's contents (verify a
// version, locate one) and `forge env deploy` advances one env's binding.
// What lives here is different in kind: moving a whole ledger from one store
// to another. It is not `forge env import` because an import is not scoped to
// an environment — it carries every env's history plus the releases those
// promotions reference, and importing one env's promotions without the
// releases they name would record bindings pointing at nothing.
//
// IMPORT IS THE ONE WAY FORWARD FROM THE RETIRED IN-CHECKOUT LEDGER. forge no
// longer reads .forge/promotions or .forge/releases, and selection REFUSES
// (ledger_unimported.go) while a checkout still holds records the selected
// store lacks — because the alternative is a versionless deploy concluding
// "never promoted" and shipping mutable tags instead of the digests those
// promotions froze. This command is what makes that refusal go quiet, and it
// goes quiet by construction: the refusal matches promotions by id, the
// import preserves ids, so once the records are in the store the comparison
// finds nothing missing. No flag, no state, nothing to remember.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// newLedgerCmd is `forge ledger`.
func newLedgerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ledger",
		Short: "Move and inspect deploy ledgers",
		Long: `Work with an entire deploy ledger — the releases a project has cut and the
promotions that bound its environments.

An environment's ledger is chosen by its DECLARATION, in one place and with no
flag: an env whose KCL declares ` + "`forge.ControlPlane`" + ` records on that control
plane; every other env records in this machine's ledger under
$FORGE_LEDGER_HOME (default ~/.forge/ledger), keyed by project so every
worktree shares one history.

FORGE_LEDGER=machine overrides that for every env and every forge process that
inherits it: nothing is read from or written to a declared control plane. Use
it for hermetic tests and scripts. $FORGE_LEDGER_HOME only relocates the
machine ledger; it never keeps a declared env off its control plane.
` + "`" + `ledger where <env>` + "`" + ` says which ledger an env uses, and why.

` + "`" + `import` + "`" + ` is how history reaches whichever store an env selected. forge once
kept promotions in ` + "`.forge/promotions/<env>.jsonl`" + ` inside the checkout; those
files are no longer read, and a command refuses while the checkout holds
records the selected ledger has never imported.`,
	}
	cmd.AddCommand(newLedgerImportCmd())
	cmd.AddCommand(newLedgerExportCmd())
	cmd.AddCommand(newLedgerWhereCmd())
	cmd.AddCommand(newLedgerShowCmd())
	return cmdutil.StrictGroup(cmd)
}

// defaultImportRev is the rev an import reads when none is named.
//
// origin/main, not HEAD. The ledger commits landed on the main branch, and a
// feature branch's HEAD can be missing the most recent ones — which would
// import a history that stops short and leave the refusal firing for the
// remainder. Reading a remote-tracking ref also means the import is about
// what the repository holds rather than what this checkout happens to be on.
const defaultImportRev = "origin/main"

// newLedgerImportCmd is `forge ledger import`.
func newLedgerImportCmd() *cobra.Command {
	var (
		fromGit        bool
		fromFileLedger string
		rev            string
		apply          bool
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a ledger from a checkout's committed history, or from another ledger directory",
		Args:  cobra.NoArgs,
		Long: `Read a ledger from somewhere else and record it in the one each environment
selected. A DRY RUN unless you pass ` + "`--apply`" + `.

TWO SOURCES:

  --from-git [--rev <ref>]        the retired in-checkout ledger
                                 (.forge/releases/*.json and
                                 .forge/promotions/<env>.jsonl) as COMMITTED at
                                 a rev. Default rev: ` + defaultImportRev + `.
  --from-file-ledger <dir>       a machine ledger directory, for an env whose
                                 KCL newly declares a control plane.

IT READS A REV, NEVER THE WORKING TREE. The retired ledger was committed, so
the repository is the authority. A ` + "`.forge/promotions`" + ` deleted but not
committed still has its history, and a half-written local edit is not history
at all.

HISTORY IS NEVER RE-JUDGED. Every record is admitted exactly as written,
including the uncomfortable parts — a release cut from a dirty tree is imported
saying so. Promotion ids and timestamps are preserved verbatim: the id is how a
re-run recognises what it already recorded, and the timestamp is the order an
environment actually moved in.

IDEMPOTENT. Running ` + "`--apply`" + ` twice records once. A run interrupted half-way
is re-run, not repaired.

IT REFUSES AN ENVIRONMENT THAT ALREADY HOLDS PROMOTIONS OF ITS OWN. An
append-only log's current binding is its last line, so interleaving imported
history with promotions made since would leave the environment reading whatever
sorted last. The remedy is to import first.

WHERE IT WRITES is each environment's selected ledger: an env whose KCL declares
` + "`forge.ControlPlane`" + ` imports INTO THAT CONTROL PLANE. The plan labels every
target as hosted or machine, and --apply names a hosted target again on the
line before it writes. To keep an import on this machine — a test, a script, a
scratch copy of a real env — set FORGE_LEDGER=machine. Setting only
$FORGE_LEDGER_HOME does not: it relocates the machine ledger, and a declared
env still imports into its control plane.

Examples:
  ` + Name() + ` ledger import --from-git                        # dry run against ` + defaultImportRev + `
  ` + Name() + ` ledger import --from-git --rev origin/main --apply
  ` + Name() + ` ledger import --from-file-ledger ~/.forge/ledger/myproj-ab12cd34 --apply
  FORGE_LEDGER=machine FORGE_LEDGER_HOME=$(mktemp -d) ` + Name() + ` ledger import --from-git --rev HEAD --apply`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLedgerImport(cmd, ledgerImportOptions{
				FromGit:        fromGit,
				FromFileLedger: fromFileLedger,
				Rev:            rev,
				Apply:          apply,
			})
		},
	}
	cmd.Flags().BoolVar(&fromGit, "from-git", false, "Read the retired in-checkout ledger (.forge/releases, .forge/promotions) as committed at --rev")
	cmd.Flags().StringVar(&fromFileLedger, "from-file-ledger", "", "Read a machine ledger directory ($FORGE_LEDGER_HOME/<project-id>/) instead")
	cmd.Flags().StringVar(&rev, "rev", defaultImportRev, "The git rev to read the checkout's ledger at (--from-git only)")
	cmd.Flags().BoolVar(&apply, "apply", false, "Record the import. Without it, print the plan and change nothing")
	return cmd
}

// ledgerImportOptions is one invocation.
type ledgerImportOptions struct {
	FromGit        bool
	FromFileLedger string
	Rev            string
	Apply          bool
}

// errImportSourceInvalid marks a request no retry can fix: no source, or two.
var errImportSourceInvalid = errors.New("invalid import source")

// runLedgerImport reads the source, plans against each env's selected
// ledger, and — with --apply — records it.
func runLedgerImport(cmd *cobra.Command, opts ledgerImportOptions) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	projectDir := projectDirForKCL()

	src, err := readImportSource(ctx, projectDir, opts)
	if err != nil {
		return err
	}
	if src.empty() {
		fmt.Fprintf(out, "Nothing to import: %s holds no releases and no promotions.\n", src.Label)
		return nil
	}

	plan, err := planLedgerImport(ctx, projectDir, src)
	if err != nil {
		return err
	}
	writeImportPlan(out, src, plan, opts.Apply)
	if len(plan.Conflicts) > 0 {
		return fmt.Errorf("refusing to import: %d environment(s) already hold promotions this import did not bring.\n"+
			"  An append-only log's current binding is its last line, so interleaving would leave the environment "+
			"reading whichever release sorted last.\n"+
			"  fix: import before promoting, or ask the ledger's owner which history is authoritative",
			len(plan.Conflicts))
	}
	if !opts.Apply {
		fmt.Fprintf(out, "\nThis was a dry run. Re-run with --apply to record it.\n")
		return nil
	}
	// The project scopes every item: a hosted env's identity is
	// (org, project, name), so an import without it would land its envs
	// in whichever project the server guessed. The machine ledger is
	// already keyed by project, so it needs nothing from this — but the
	// payload is one type for both stores, and leaving the field empty
	// for the local case would make the hosted path's requirement look
	// optional.
	return applyLedgerImport(ctx, out, plan, hostedProjectName())
}

// readImportSource resolves exactly one source.
func readImportSource(ctx context.Context, projectDir string, opts ledgerImportOptions) (ledgerSource, error) {
	switch {
	case opts.FromGit && opts.FromFileLedger != "":
		return ledgerSource{}, fmt.Errorf("%w: --from-git and --from-file-ledger name two different ledgers; pick one", errImportSourceInvalid)
	case opts.FromGit:
		rev := opts.Rev
		if strings.TrimSpace(rev) == "" {
			rev = defaultImportRev
		}
		return readGitLedger(ctx, projectDir, rev)
	case opts.FromFileLedger != "":
		return readFileLedger(opts.FromFileLedger)
	default:
		return ledgerSource{}, fmt.Errorf("%w: name a source — --from-git (the checkout's committed ledger) or --from-file-ledger <dir>", errImportSourceInvalid)
	}
}

// ─── The plan ───────────────────────────────────────────────────────────────

// ledgerImportPlan is what an import would do, grouped by the ledger each
// env selected.
//
// GROUPED BY TARGET, not flat, because selection is per env: one project can
// have prod on a control plane and dev on this machine, and each env's
// records must land in the store its own declaration chose. A flat plan would
// have to re-resolve the target at write time and could resolve it
// differently than it reported.
type ledgerImportPlan struct {
	// Targets are the per-ledger units of work, ordered by the first env
	// that selected each one, so the plan reads in a stable order.
	Targets []*ledgerImportTarget
	// Conflicts name the envs that already hold promotions this import did
	// not bring, with the reason.
	Conflicts []string
}

// ledgerImportTarget is everything destined for ONE ledger.
type ledgerImportTarget struct {
	// Location is the ledger's human-facing address.
	Location string
	// Hosted reports a control-plane ledger: a write here leaves this
	// machine, so the plan labels it and the apply names it again on the
	// line before it writes.
	Hosted bool
	// Overridden maps an env to the control plane it DECLARES, for each
	// env FORGE_LEDGER=machine kept on this machine's ledger instead.
	Overridden map[string]string
	// Importer records into it.
	Importer ledgerImporter
	// Envs are the environments whose promotions land here, sorted.
	Envs []string
	// Releases are the releases this ledger does not already hold.
	Releases []sourceRelease
	// Promotions are per env, oldest first, excluding ids already present.
	Promotions map[string][]sourcePromotion
	// AlreadyHeld counts what a previous import already recorded — the
	// number that makes a second --apply visibly a no-op rather than
	// silently one.
	AlreadyHeldReleases   int
	AlreadyHeldPromotions int
}

// counts is what the plan prints for one target.
func (t *ledgerImportTarget) counts() (releases, promotions int) {
	for _, ps := range t.Promotions {
		promotions += len(ps)
	}
	return len(t.Releases), promotions
}

// ledgerImporter is the capability an import needs of a ledger, declared
// HERE at the consumer: record a release, record a promotion verbatim with
// its source, and say what is already held.
//
// THREE METHODS, AND THE THIRD IS WHY IT IS A SEPARATE INTERFACE RATHER THAN
// fields on envLedger. An import must know what the target ALREADY holds
// before it writes anything — that is the whole dry run, and it is what
// separates "already imported" from "promoted since", which is the refusal.
// Expressing it through bindingStore.Current and releaseLedger.List would
// answer only "what is current", not "which ids are present".
//
// Both backends satisfy it: the machine store directly, and the hosted one
// through the ImportLedger RPC (hosted_import.go, F4) — one round trip
// carrying the whole payload, which is why Import takes an assembled
// *ledgerImport rather than one record at a time.
//
// THE PAYLOAD TYPE IS F4's, not a parallel one. `ledgerImport` and
// `ledgerImportResult` are the hosted wire's vocabulary, and the machine
// store reads the same assembled value. That is the one-model-two-stores
// doctrine applied to the import itself: a second payload type here would be
// free to carry a field the wire drops, and the difference would only show up
// as a column that is silently empty in an immutable row.
type ledgerImporter interface {
	// Held is every release version and promotion id the ledger already
	// has, so the plan can subtract them and spot the ordering conflict.
	Held(ctx context.Context, envs []string) (ledgerHeld, error)
	// Import records (or, with the payload's dry-run flag, plans) the
	// import. It is idempotent on release version and promotion id, so a
	// re-run after a partial failure converges.
	Import(ctx context.Context, in *ledgerImport) (ledgerImportResult, error)
}

// ledgerHeld is what a ledger already contains.
type ledgerHeld struct {
	// ReleaseVersions is every version the ledger holds.
	ReleaseVersions map[string]struct{}
	// PromotionIDs is every promotion id, per env.
	PromotionIDs map[string]map[string]struct{}
}

// planLedgerImport resolves each env's ledger, subtracts what is already
// held, and finds the conflicts.
func planLedgerImport(ctx context.Context, projectDir string, src ledgerSource) (ledgerImportPlan, error) {
	var plan ledgerImportPlan
	byLocation := map[string]*ledgerImportTarget{}

	for _, env := range src.envs() {
		// selectLedger, NOT ledgerFor: ledgerFor runs the
		// unimported-checkout refusal, and this command is the thing
		// that clears it. Going through the refusal would make the
		// import refuse to run for exactly the reason it exists.
		l, err := selectLedger(ctx, projectDir, env)
		if err != nil {
			return plan, fmt.Errorf("choose the ledger for env %q: %w", env, err)
		}
		importer, err := importerFor(l)
		if err != nil {
			return plan, fmt.Errorf("env %q: %w", env, err)
		}
		location := l.Bindings.Location()
		target := byLocation[location]
		if target == nil {
			target = &ledgerImportTarget{
				Location:   location,
				Hosted:     l.Hosted,
				Importer:   importer,
				Promotions: map[string][]sourcePromotion{},
			}
			byLocation[location] = target
			plan.Targets = append(plan.Targets, target)
		}
		target.Envs = append(target.Envs, env)
		if l.OverriddenEndpoint != "" {
			if target.Overridden == nil {
				target.Overridden = map[string]string{}
			}
			target.Overridden[env] = l.OverriddenEndpoint
		}
	}

	// The releases go to EVERY target an env of this project selected,
	// because a promotion names a release and a ledger that holds the
	// promotion without the release records a binding pointing at nothing.
	// A release is keyed and idempotent, so recording it in two stores
	// duplicates nothing that matters.
	for _, target := range plan.Targets {
		sort.Strings(target.Envs)
		held, err := target.Importer.Held(ctx, target.Envs)
		if err != nil {
			return plan, fmt.Errorf("read what %s already holds: %w", target.Location, err)
		}
		releases, alreadyHeld := releasesToSend(src, target.Envs, held)
		target.AlreadyHeldReleases += alreadyHeld
		target.Releases = append(target.Releases, releases...)
		for _, env := range target.Envs {
			sourceIDs := map[string]struct{}{}
			for _, p := range src.Promotions[env] {
				sourceIDs[p.Promotion.ID] = struct{}{}
			}
			// A promotion the ledger holds that the source does not
			// is the ambiguous-ordering case (§11.1 step 6): the env
			// moved since, and interleaving imported history with it
			// would leave the binding reading whichever line sorted
			// last.
			var foreign []string
			for id := range held.PromotionIDs[env] {
				if _, fromSource := sourceIDs[id]; !fromSource {
					foreign = append(foreign, id)
				}
			}
			if len(foreign) > 0 {
				sort.Strings(foreign)
				plan.Conflicts = append(plan.Conflicts, fmt.Sprintf(
					"%s: %s already holds %d promotion(s) this import did not bring (%s)",
					env, target.Location, len(foreign), strings.Join(shortIDs(foreign), ", ")))
				continue
			}
			for _, p := range src.Promotions[env] {
				if _, ok := held.PromotionIDs[env][p.Promotion.ID]; ok {
					target.AlreadyHeldPromotions++
					continue
				}
				target.Promotions[env] = append(target.Promotions[env], p)
			}
		}
	}
	sort.Strings(plan.Conflicts)
	return plan, nil
}

// releasesToSend is the releases the payload carries to one ledger, and how
// many the ledger already held.
//
// A held release is subtracted EXCEPT when a promotion this import still has
// to bring names it. The hosted ImportLedger resolves a promotion's release
// from the SAME request, so omitting a release the control plane already holds
// (a deploy's bundle step records its release there first) made the promotion
// fail with "names release X, which this import does not include". Re-sending
// it is idempotent: the store finds the existing row and checks the artifacts
// agree.
func releasesToSend(src ledgerSource, envs []string, held ledgerHeld) (send []sourceRelease, alreadyHeld int) {
	named := map[string]bool{}
	for _, env := range envs {
		for _, p := range src.Promotions[env] {
			if _, ok := held.PromotionIDs[env][p.Promotion.ID]; !ok {
				named[p.Promotion.Release] = true
			}
		}
	}
	for _, r := range src.Releases {
		if _, ok := held.ReleaseVersions[r.Release.Version]; ok {
			alreadyHeld++
			if !named[r.Release.Version] {
				continue
			}
		}
		send = append(send, r)
	}
	return send, alreadyHeld
}

// shortIDs trims promotion ids for an error message, keeping at most three
// so a long-diverged env does not print a wall of hex.
func shortIDs(ids []string) []string {
	const max = 3
	out := make([]string, 0, max+1)
	for i, id := range ids {
		if i == max {
			out = append(out, fmt.Sprintf("and %d more", len(ids)-max))
			break
		}
		out = append(out, id)
	}
	return out
}

// importerFor is the import capability of an env's ledger, or a refusal
// naming what is missing.
func importerFor(l envLedger) (ledgerImporter, error) {
	if importer, ok := l.Bindings.(ledgerImporter); ok {
		return importer, nil
	}
	return nil, fmt.Errorf("the ledger at %s cannot accept an import "+
		"(it is reachable for reads but exposes no import)", l.Bindings.Location())
}

// ─── Applying ───────────────────────────────────────────────────────────────

// applyLedgerImport records every target's payload.
func applyLedgerImport(ctx context.Context, out io.Writer, plan ledgerImportPlan, project string) error {
	for _, target := range plan.Targets {
		if target.Hosted {
			// Named on the line BEFORE the write, not only in the plan
			// above it: a write that leaves this machine is the one an
			// operator must be able to stop, and the line they read last
			// before it happens is this one.
			fmt.Fprintf(out, "Writing to the HOSTED ledger at %s (environments: %s) …\n",
				target.Location, strings.Join(target.Envs, ", "))
		}
		result, err := target.Importer.Import(ctx, target.payload(project))
		if err != nil {
			return fmt.Errorf("import into %s: %w.\n"+
				"  The import is idempotent: re-run it to continue from where it stopped", target.Location, err)
		}
		fmt.Fprintf(out, "Imported into %s: %s.\n", target.Location, describeImportCounts(result.Counts))
		for _, conflict := range result.Conflicts {
			// A non-empty conflict list with no error is the hosted
			// import's normal partial outcome: the transaction
			// committed what it could name unambiguously. It is not
			// an error here either — re-running is the remedy, and
			// the import is idempotent — but it must be VISIBLE,
			// because the alternative is an operator believing the
			// whole history landed.
			fmt.Fprintf(out, "  declined: %s\n", conflict)
		}
	}
	fmt.Fprintf(out, "\nVerify with `%s env status <env>` and `%s ledger export --dir <tmp>`.\n", Name(), Name())
	return nil
}

// payload assembles the target's records into the import F4's wire accepts.
//
// It goes through the Add* methods rather than a struct literal for the
// reason hosted_import.go states: every item needs its `imported_from`,
// which is the per-record dedupe key the whole import's idempotency rests
// on, and a method signature makes omitting it impossible.
func (t *ledgerImportTarget) payload(project string) *ledgerImport {
	in := newLedgerImport(project, "forge ledger import", false)
	// RELEASES FIRST, and not for tidiness. A promotion names a release,
	// so a ledger holding the promotion without the release records a
	// binding pointing at nothing — and an import interrupted between the
	// two halves must leave a state a re-run can finish, not one where
	// prod claims a release the ledger never heard of.
	for _, r := range t.Releases {
		in.AddRelease(r.Release, r.From)
	}
	for _, env := range t.Envs {
		// A retired env (control-plane's staging and preprod, O-9) is
		// imported as soft-deleted on a hosted ledger, so its history
		// stays queryable while it is absent from Live and its name is
		// not reserved. DeletedAt is nil here and the retirement is
		// the operator's call — forge cannot tell "retired" from "not
		// declared on this branch" from a checkout.
		in.AddEnvironment(env, release.EnvSelfManaged, nil, t.envSource(env))
		for _, p := range t.Promotions[env] {
			// promoted_at EXPLICITLY, from the record: the whole
			// point of an import is that these already happened.
			in.AddPromotion(p.Promotion, p.Promotion.PromotedAt, p.From)
		}
	}
	return in
}

// envSource is the provenance for an env row the import synthesized: the
// first promotion's source, since the env itself has no file of its own in
// the retired layout.
func (t *ledgerImportTarget) envSource(env string) string {
	if ps := t.Promotions[env]; len(ps) > 0 {
		return ps[0].From
	}
	return "forge ledger import"
}

// describeImportCounts renders the per-table counts a backend reports.
//
// The counts are a MAP because the server owns which tables it counted
// (hosted_import.go says so), and the machine store answers in the same
// vocabulary rather than in named fields — so one renderer serves both and
// a backend that starts counting bundles needs no change here.
func describeImportCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "nothing"
	}
	tables := make([]string, 0, len(counts))
	for table := range counts {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		parts = append(parts, fmt.Sprintf("%d %s", counts[table], table))
	}
	return strings.Join(parts, ", ")
}

// ─── The machine ledger's importer ──────────────────────────────────────────

// Held reads the machine ledger's release versions and per-env promotion ids.
func (s machineBindingStore) Held(_ context.Context, envs []string) (ledgerHeld, error) {
	held := ledgerHeld{
		ReleaseVersions: map[string]struct{}{},
		PromotionIDs:    map[string]map[string]struct{}{},
	}
	releases, err := s.store.Releases()
	if err != nil {
		return held, err
	}
	for _, r := range releases {
		held.ReleaseVersions[r.Version] = struct{}{}
	}
	for _, env := range envs {
		ids := map[string]struct{}{}
		promotions, err := s.store.Promotions(env)
		if err != nil {
			return held, err
		}
		for _, p := range promotions {
			if p.ID != "" {
				ids[p.ID] = struct{}{}
			}
		}
		held.PromotionIDs[env] = ids
	}
	return held, nil
}

// Import records the assembled import in the machine ledger, in the order
// the payload holds it: releases, then each env's row, then its promotions.
//
// IT READS THE SAME *ledgerImport THE HOSTED WIRE SENDS, which is what makes
// "one model, two stores" true of the import and not just of the records. The
// fields are unexported, so this is the one place outside hosted_import.go
// that reads them — a deliberate exception to the package-boundary rule,
// taken because the alternative (a second payload type, or exported getters
// for five slices) would be a parallel vocabulary free to drift from the
// wire's.
//
// Each record goes through the store's own lock, one at a time, rather than
// one lock for the whole import. That costs a flock per record and buys the
// property that matters here: the import is resumable at record granularity,
// so a failure half-way through 16 promotions re-runs and skips the 9 it
// already wrote. A single critical section would instead have to be
// all-or-nothing, which a file ledger cannot actually deliver — there is no
// rollback for an append that already hit the disk. The hosted side gets a
// real transaction, and that asymmetry is honest rather than papered over.
func (s machineBindingStore) Import(_ context.Context, in *ledgerImport) (ledgerImportResult, error) {
	result := ledgerImportResult{Counts: map[string]int{"releases": 0, "promotions": 0}}
	for _, r := range in.releases {
		// A release cut before releases carried a project keeps "" —
		// history is not re-judged, and inventing a project for a row
		// that never had one would be a guess recorded as a fact.
		created, err := s.store.CutRelease(r.Release)
		if err != nil {
			return result, fmt.Errorf("record release %s (from %s): %w", r.Release.Version, r.ImportedFrom, err)
		}
		if created {
			result.Counts["releases"]++
		}
	}
	for _, e := range in.environments {
		if err := s.recordImportedEnv(e.Name, e.Kind); err != nil {
			return result, err
		}
	}
	for _, p := range in.promotions {
		if err := s.store.ImportPromotionFrom(p.Promotion, p.ImportedFrom); err != nil {
			return result, fmt.Errorf("record promotion %s of %s (from %s): %w",
				p.Promotion.ID, p.Promotion.Env, p.ImportedFrom, err)
		}
		result.Counts["promotions"]++
	}
	return result, nil
}

// recordImportedEnv gives an imported env a record in the ledger, so a
// reader listing environments sees it without re-rendering the project.
//
// The kind is NOT guessed. An env the import brought history for may be
// RETIRED — control-plane's staging and preprod have no KCL at all any more
// (O-9) — so there is nothing to render and no declaration to read. A record
// already present is left alone: a declaration recorded by `forge env build`
// knows the kind and the shape, and an import must not overwrite it with
// less.
func (s machineBindingStore) recordImportedEnv(env string, kind release.EnvKind) error {
	existing, err := s.store.Env(env)
	if err != nil {
		return fmt.Errorf("read env record %q: %w", env, err)
	}
	if existing != nil {
		return nil
	}
	// The caller supplies the kind, and for an import it is
	// release.EnvSelfManaged — the honest kind for an env whose history
	// came from a git ledger: it was promoted (so not LOCAL) and nothing
	// placed it (so not persistent). §11.1 step 1 and step 3 both name it.
	return s.store.PutEnv(release.EnvRecord{Name: env, Kind: kind})
}

// ─── The hosted ledger's importer ───────────────────────────────────────────

// Held reads what the control plane already holds, so the dry run can
// subtract it and the plan can spot the ordering conflict.
//
// IT IS AN ADVISORY READ, NOT THE AUTHORITY, and that is the difference from
// the machine store. The server judges both refusals — an env of kind
// persistent or preview, and an env already holding a non-imported promotion
// — INSIDE the import transaction, against rows that can change under a
// client-side read (hosted_import.go says so). This read exists so the dry
// run can show an operator the plan before they commit to it, not so forge
// can pre-approve the write. When the two disagree, the server wins and its
// conflicts are printed.
//
// An env the control plane has never heard of has, by definition, no
// promotions — the same rule HistoryPage applies — so a failed lookup is an
// empty set rather than an error. That is the normal state for the import
// that is BOOTSTRAPPING those env rows (§11.1 step 1).
func (s *hostedStore) Held(ctx context.Context, envs []string) (ledgerHeld, error) {
	held := ledgerHeld{
		ReleaseVersions: map[string]struct{}{},
		PromotionIDs:    map[string]map[string]struct{}{},
	}
	releases, err := s.List(ctx)
	if err != nil {
		return held, err
	}
	for _, r := range releases {
		held.ReleaseVersions[r.Version] = struct{}{}
	}
	for _, env := range envs {
		ids := map[string]struct{}{}
		page, err := s.HistoryPage(ctx, env, historyQuery{Limit: maxHistoryLimit})
		if err != nil {
			// A never-ensured env reads as no history, which is what
			// an import into a control plane that has not heard of
			// the project must see.
			held.PromotionIDs[env] = ids
			continue
		}
		for {
			for _, p := range page.Promotions {
				if p.ID != "" {
					ids[p.ID] = struct{}{}
				}
			}
			if page.Next == "" {
				break
			}
			page, err = s.HistoryPage(ctx, env, historyQuery{Limit: maxHistoryLimit, Before: page.Next})
			if err != nil {
				return held, fmt.Errorf("page %s's promotions at %s: %w", env, s.endpoint, err)
			}
		}
		held.PromotionIDs[env] = ids
	}
	return held, nil
}

// Import sends the whole payload in ONE round trip, which the server applies
// in ONE transaction (F4's ImportLedger).
//
// The transaction is the reason the hosted path is stronger than the machine
// one: releases, env rows and promotions either all land or none do, and the
// server assigns ids and timestamps from the records rather than the clock.
// forge's job here is only to hand over the assembled import and report what
// came back.
func (s *hostedStore) Import(ctx context.Context, in *ledgerImport) (ledgerImportResult, error) {
	return hostedImportClient{client: s.client}.ImportLedger(ctx, in)
}

// Compile-time proof that BOTH backends can be imported into. The assertions
// are what keep a method rename from silently dropping a store out of the
// capability `forge ledger import` type-asserts for — which would surface as
// "this ledger cannot accept an import" against a control plane that can.
var (
	_ ledgerImporter = machineBindingStore{}
	_ ledgerImporter = (*hostedStore)(nil)
)

// ─── Reporting ──────────────────────────────────────────────────────────────

// writeImportPlan prints what the import holds and what it would do.
//
// THE COUNTS ARE THE VERIFICATION. §11.1 step 2's dry run is how an operator
// checks that the import found the whole history before recording it — "39
// releases, 16 promotions, 3 envs" against what they know the repository
// holds. So the numbers come first, per target, with what is already held
// shown separately: that is what makes a second --apply visibly a no-op
// rather than silently one.
func writeImportPlan(out io.Writer, src ledgerSource, plan ledgerImportPlan, apply bool) {
	verb := "Would import"
	if apply {
		verb = "Importing"
	}
	fmt.Fprintf(out, "Read %s: %d release(s), %d promotion(s) across %d environment(s) (%s).\n",
		src.Label, len(src.Releases), src.promotionCount(), len(src.Promotions), strings.Join(src.envs(), ", "))

	for _, target := range plan.Targets {
		releases, promotions := target.counts()
		fmt.Fprintf(out, "\n%s into %s\n", verb, target.Location)
		writeImportTargetLedger(out, target)
		fmt.Fprintf(out, "  environments: %s\n", strings.Join(target.Envs, ", "))
		fmt.Fprintf(out, "  releases:     %d new", releases)
		if target.AlreadyHeldReleases > 0 {
			fmt.Fprintf(out, " (%d already held)", target.AlreadyHeldReleases)
		}
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  promotions:   %d new", promotions)
		if target.AlreadyHeldPromotions > 0 {
			fmt.Fprintf(out, " (%d already held)", target.AlreadyHeldPromotions)
		}
		fmt.Fprintln(out)
		for _, env := range target.Envs {
			if ps := target.Promotions[env]; len(ps) > 0 {
				last := ps[len(ps)-1].Promotion
				fmt.Fprintf(out, "    %s: %d promotion(s), ending at %s\n", env, len(ps), last.Release)
			}
		}
	}
	for _, conflict := range plan.Conflicts {
		fmt.Fprintf(out, "\nCONFLICT %s\n", conflict)
	}
}

// writeImportTargetLedger labels a target as hosted or machine, and why.
//
// The label exists because the location alone did not prevent the surprise
// it was meant to: a URL among the plan's lines reads as a detail, and a
// script that believed $FORGE_LEDGER_HOME kept it private imported into a
// production control plane while printing that URL.
func writeImportTargetLedger(out io.Writer, target *ledgerImportTarget) {
	if target.Hosted {
		fmt.Fprintf(out, "  ledger:       HOSTED — the control plane these environments declare (forge.ControlPlane)\n")
		if strings.TrimSpace(os.Getenv(ledgerfile.DefaultHomeEnv)) != "" {
			fmt.Fprintf(out, "  note:         $%s relocates this machine's ledger; it does not keep a declared env off its control plane. %s=%s does\n",
				ledgerfile.DefaultHomeEnv, ledgerSelectionEnv, ledgerSelectMachine)
		}
		return
	}
	fmt.Fprintf(out, "  ledger:       this machine's\n")
	envs := make([]string, 0, len(target.Overridden))
	for env := range target.Overridden {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	for _, env := range envs {
		fmt.Fprintf(out, "  override:     %s=%s keeps %s here; it declares the control plane at %s, which this import does not touch\n",
			ledgerSelectionEnv, ledgerSelectMachine, env, target.Overridden[env])
	}
}
