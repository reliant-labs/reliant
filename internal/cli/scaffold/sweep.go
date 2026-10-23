// File: internal/cli/scaffold/sweep.go
//
// `forge scaffold` with NO arguments — the one command after proto edits:
// "I added protos, now scaffold me all the things." Arity is the mode
// selector: bare `forge scaffold` runs the sweep below; `forge scaffold
// <noun> ...` (scaffold.go and friends) scaffolds exactly one thing.
//
// The sweep is a VISIBLE, phased chain of the existing scaffold-once
// primitives, never a new writer:
//
//	Phase 1  entity births — every `// forge:entity`-marked message with
//	         no applied table gets its missing CRUD quintet injected
//	         (one-time, entity.go's own piece builders) and its owned
//	         migration (internal/scaffold's renderer, fed from the
//	         RAW proto — a brand-new message need not be in the
//	         descriptor). Already-tabled marked messages are INERT
//	         (reported; evolution is a new migration). Envelope-shaped
//	         marked messages are refused loudly — the marker never
//	         overrides the guard. One bad entity never aborts the batch.
//	Phase 2  projection — the generate pipeline, in-process (the same
//	         self-heal mechanism `forge scaffold rpc` uses), run when
//	         phase 1 wrote anything or the descriptor is stale w.r.t.
//	         the raw protos; skipped loudly otherwise. The projection emits
//	         the pb-through handler stub for every new custom RPC and the
//	         CRUD wiring for every entity-backed one.
//	Summary  one table: entities birthed / quintets completed /
//	         skipped+why / TODO fields carried.
//
// Everything written is scaffold-once owned (WriteScaffoldIfMissing,
// plain migration files, one-time proto injection) — zero new Tier-1
// surface. `--dry-run` prints the plan (phase-1 predictions) and writes
// nothing. Idempotent: a re-run with nothing missing is a clean no-op
// summary.

package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/naming"
)

func init() { factory.Register(newScaffoldCmd) }

// newScaffoldCmd builds `forge scaffold` — the single verb for writing new
// code into a project, with ARITY picking the granularity:
//
//	forge scaffold                     sweep: birth everything the protos imply
//	forge scaffold <noun> [args...]    one thing, explicitly
//
// Args is cobra.NoArgs so an unrecognised noun (`forge scaffold widget`) is
// rejected as an unknown command instead of silently falling through to the
// sweep. The nouns attach via addNounCmds (scaffold.go).
func newScaffoldCmd(f *factory.Factory) *cobra.Command {
	var serviceFlag string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "scaffold",
		Short: "Scaffold code: bare, everything the protos imply; with a noun, exactly one thing",
		Long: `Scaffold code into a Forge project. Arity picks the mode.

With NO arguments it scaffolds everything the protos imply, in one
visible, phased run. Author the wire truth first — messages (marking
entities with a leading ` + "`// forge:entity`" + ` comment) and custom RPCs —
then run it:

  Phase 1: entity births. Every marked message with no applied table gets
           its missing CRUD quintet injected into the service proto
           (one-time) and an owned create-table migration. A marked
           message whose table already exists is INERT (evolution is a
           new migration); envelope shapes (Request/Response names,
           pagination fields) are refused loudly — the marker never
           overrides the guard.
  Phase 2: projection. Runs the generate pipeline in-process when phase 1
           wrote anything or the descriptor is stale against the raw
           protos; skipped loudly otherwise. Projection emits the
           pb-through handler stub for every new custom RPC (a method on
           the handler *Service returning Unimplemented) and the CRUD
           wiring for every entity-backed one.

With a NOUN it scaffolds exactly that one thing, no marker required:

  forge scaffold entity <name> --from-proto <svc> one DB entity, from its authored proto message
  forge scaffold service <name>                   a new Go service
  forge scaffold worker <name>                    a background worker
  forge scaffold operator <name> [--group G] [--version V]   a Kubernetes operator
  forge scaffold crd <Name>                       a CRD + reconciler on an operator
  forge scaffold binary <name>                    a non-server long-running binary
  forge scaffold frontend <name>                  a Next.js frontend
  forge scaffold scenario <name>                  a frontend mock scenario
  forge scaffold webhook <name> --service S       a webhook endpoint on a service
  forge scaffold package <name>                   an internal package (alias for ` + "`forge package new`" + `)
  forge scaffold adapter <name>                   an outbound adapter (HTTP/queue/storage gateway)
  forge scaffold library <name>                   a library-shaped package (no contract.go; pre-excluded)
  forge scaffold handler-file <svc> <name>        an additional RPC-group file in handlers/<svc>/
  forge scaffold rpc <svc> <Name>                 a custom RPC: pb-through *Service stub when the RPC is in the proto; signed stub + proto snippet otherwise

Everything written is scaffold-once and yours from birth — re-running
with nothing missing is a clean no-op. Births are one-time: after birth,
no command ever writes or modifies a migration from proto state.

Examples:
  forge scaffold
  forge scaffold --service tasks
  forge scaffold --dry-run
  forge scaffold entity product --from-proto tasks
  forge scaffold service orders
  forge scaffold rpc tasks ArchiveTask`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSweep(f, serviceFlag, dryRun)
		},
	}
	cmd.Flags().StringVar(&serviceFlag, "service", "", "narrow every phase of the sweep to one service")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the full plan (phase 1+2 predictions) and write nothing")
	addNounCmds(cmd, f)
	holdProjectLock(cmd, f)
	// Every noun writes generated and state files: set up the clone's
	// merge driver and hooks for them.
	return cmdutil.ConfiguresClone(cmd)
}

// sweepSummary accumulates the phase outcomes for the final
// table.
// sweepSummary is the tally `forge scaffold` reports at the end.
//
// Inert and Refused are separate because they are opposite news, and summing
// them into one "skipped: N" made the sweep's verdict unreadable: 8 inert
// markers is the steady state of every re-run (nothing to do, correctly),
// while 8 refused markers means the project asked for 8 entities and got
// none. Both used to print "✅ nothing was missing — clean no-op".
type sweepSummary struct {
	EntitiesBirthed   []string // "pkg.Msg → table"
	QuintetsCompleted int
	// ManagedFields counts the id/created_at/updated_at/deleted_at fields
	// birth declared on the authors' entity messages.
	ManagedFields int
	Inert         []string // marker with an applied table already — nothing to do
	Refused       []string // marker forge declined to birth — a real gap
	Skipped       []string // other per-item skips (quintet completion, …)
	Failed        []string // "item — err"
	TodoFields    int
}

func runSweep(f *factory.Factory, svcFilter string, dryRun bool) error { //nolint:funlen // the sweep IS the phase sequence: phase-1 births, phase-2 projection, summary. Splitting it hides the ordering that is the whole contract; the phases share one accumulator and one dry-run switch.
	ctxLabel := "forge scaffold"

	root, err := projectRoot()
	if err != nil {
		return err
	}
	configPath := filepath.Join(root, "forge.yaml")
	cfg, err := generator.ReadProjectConfig(configPath)
	if err != nil {
		return cliutil.WrapUserErr(ctxLabel, "read project config", configPath,
			"verify forge.yaml is valid YAML", err)
	}
	if !cfg.IsServiceKind() {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("`forge scaffold` is only available for service projects (this project's kind: %s)", cfg.EffectiveKind()),
			"", "re-run `forge project new` with --kind service to scaffold a server")
	}

	svcLeaves, err := scaffoldServiceLeaves(ctxLabel, root, svcFilter)
	if err != nil {
		return err
	}

	// The compiled descriptor — best-effort here; phase 2 owns refreshing
	// it, and phase 1 reads the RAW protos, not this. `declared` answers
	// "is the descriptor there at all"; `services` is what forge manages.
	declared, _ := codegen.ParseDeclaredServices(root)
	services := codegen.ManagedServices(declared)

	migDir := filepath.Join(root, "db", "migrations")
	applied, existing := appliedSchema(migDir)
	fkReg := newFKRegistry(existing)
	summary := &sweepSummary{}

	// Raw scans, one per service dir — shared by phases 1 and 2. A service
	// marked `// forge:unmanaged` is left out of both: births would inject
	// CRUD rpcs into a service whose handlers forge does not own, and the
	// projection generates nothing for it anyway.
	scans := map[string]*codegen.RawProtoScan{}
	for _, leaf := range svcLeaves {
		scan, serr := codegen.ScanRawProtoDir(filepath.Join(root, "proto", "services", leaf))
		if serr != nil {
			continue
		}
		if scan.ServiceName != "" && scan.ServiceUnmanaged(scan.ServiceName) {
			note := fmt.Sprintf("proto/services/%s: %s is marked // %s — skipped (delete the marker to adopt it)",
				leaf, scan.ServiceName, codegen.ProtoMarkerUnmanaged)
			fmt.Printf("  ⏭️  %s\n", note)
			summary.Skipped = append(summary.Skipped, note)
			continue
		}
		scans[leaf] = scan
	}

	// Pre-flight: a proto service whose name disagrees with its directory
	// generates two different Go identifiers and cannot build.
	//
	// Phase 2 catches this too, but by then phase 1 has already written a
	// migration per birthed entity. The revert is clean and the re-run
	// is a correct no-op, so nothing breaks — it just costs a full cycle to
	// learn a fact that is a string comparison over data already in hand.
	// Checked here, before anything is written.
	for _, leaf := range svcLeaves {
		scan := scans[leaf]
		if scan == nil || scan.ServiceName == "" {
			continue
		}
		if verr := cmdutil.ValidateServiceDirConsistency(scan.ServiceName, leaf); verr != nil {
			return cliutil.UserErr(ctxLabel, verr.Error(), "",
				"rename the proto service or the directory so both generators agree, then re-run")
		}
	}

	// FK-suggestion registry for phase-1 births: the applied schema plus
	// every table this scaffold births — `// forge:entity` messages across
	// all service scans plus any descriptor CRUD entities. Built once so
	// each birth resolves its `<x>_id` columns against the same real-table
	// set (an Order born alongside a Provider suggests REFERENCES providers;
	// a bare user_id with no User entity gets no suggestion).
	allScans := make([]*codegen.RawProtoScan, 0, len(scans))
	for _, leaf := range svcLeaves {
		if s := scans[leaf]; s != nil {
			allScans = append(allScans, s)
		}
	}
	knownTables := fkKnownTables(applied, codegen.ServiceDef{}, allScans...)
	for _, svc := range services {
		for t := range fkKnownTables(nil, svc) {
			knownTables[t] = true
		}
	}

	// ── Phase 1: entity births ────────────────────────────────────────
	fmt.Println("── Phase 1: entity births (// forge:entity markers) ─────────────")
	birthsWrote := false
	birthsPlanned := 0
	for _, leaf := range svcLeaves {
		scan := scans[leaf]
		if scan == nil {
			continue
		}
		marked, skips := selectMarkedFromRawScan(scan, applied, nil)
		for _, s := range skips {
			fmt.Printf("  ⏭️  %s\n", s.Note)
			if s.Inert {
				summary.Inert = append(summary.Inert, s.Note)
				continue
			}
			summary.Refused = append(summary.Refused, s.Note)
		}
		for _, m := range marked {
			table := naming.Pluralize(naming.ToSnakeCase(m.Name))
			if dryRun {
				birthsPlanned++
				fmt.Printf("  📋 would birth %s → table %q (migration)\n", m.Package+"."+m.Name, table)
				missRPCs, missMsgs := predictQuintetCompletion(scan, m.Name, m.AppendOnly)
				fmt.Print("   ")
				printQuintetPlan(scan, m.Name, missRPCs, missMsgs)
				continue
			}
			fmt.Printf("  🔧 %s → %s\n", m.Package+"."+m.Name, table)
			rep, berr := birthMarkedEntity(migDir, root, scan, m, knownTables, entityOpts{}, fkReg)
			if berr != nil {
				line := fmt.Sprintf("%s — %v", m.Name, berr)
				summary.Failed = append(summary.Failed, line)
				fmt.Printf("     ⚠️  %s (continuing)\n", line)
				continue
			}
			birthsWrote = true
			summary.EntitiesBirthed = append(summary.EntitiesBirthed, rep.Message+" → "+rep.Table)
			summary.TodoFields += rep.TodoFields
			fmt.Println(birthMigrationLine(root, rep.UpPath))
			switch {
			case rep.ManagedFieldsErr != nil:
				line := fmt.Sprintf("%s managed fields — %v", m.Name, rep.ManagedFieldsErr)
				summary.Failed = append(summary.Failed, line)
				fmt.Printf("     ⚠️  %s (declare id/created_at/updated_at by hand)\n", line)
			case len(rep.ManagedFields) > 0:
				summary.ManagedFields += len(rep.ManagedFields)
				fmt.Printf("     ✅ managed fields declared on %s: %s\n", m.Name, strings.Join(rep.ManagedFields, ", "))
			}
			switch {
			case rep.QuintetErr != nil:
				line := fmt.Sprintf("%s quintet completion — %v", m.Name, rep.QuintetErr)
				summary.Failed = append(summary.Failed, line)
				fmt.Printf("     ⚠️  %s (the migration landed; add the CRUD surface by hand)\n", line)
			case rep.Quintet == nil:
				note := fmt.Sprintf("%s: service proto not found — quintet completion skipped", m.Name)
				summary.Skipped = append(summary.Skipped, note)
				fmt.Printf("     ⏭️  %s\n", note)
			case rep.Quintet.Complete():
				fmt.Printf("     ✅ CRUD quintet already complete\n")
			default:
				summary.QuintetsCompleted++
				fmt.Printf("     ✅ quintet completed in %s (+%d rpc, +%d message)\n",
					filepath.Base(rep.Quintet.ProtoPath), len(rep.Quintet.AddedRPCs), len(rep.Quintet.AddedMessages))
			}
			for _, n := range rep.Notes {
				fmt.Printf("     ℹ️  %s\n", n)
			}
		}
	}
	if !birthsWrote && birthsPlanned == 0 && summary.nothingHappened() {
		fmt.Println("  ✅ nothing to birth — no marked message is missing its table")
	}

	// ── Phase 2: projection ───────────────────────────────────────────
	fmt.Println("\n── Phase 2: projection (forge generate) ─────────────────────────")
	staleRPCs := staleRawRPCs(scans, services)
	// A prior generate that failed its own validation REVERTED the
	// projection (handlers, ops, wiring) but not gen/ — external outputs
	// like the compiled descriptor are deliberately outside the rollback
	// journal. After such a run the descriptor is a LYING freshness
	// signal: it already knows every RPC while the sources that implement
	// them are gone from disk. The preserved-failure directory is the
	// durable marker of that state — while it exists, the projection must
	// re-run (and a successful generate cleans the marker up).
	prevFailed := false
	if _, statErr := os.Stat(filepath.Join(root, ".forge", "failed-generate")); statErr == nil {
		prevFailed = true
	}
	needGenerate := birthsWrote || len(staleRPCs) > 0 || len(declared) == 0 || prevFailed
	reason := generateReason(birthsWrote, staleRPCs, len(declared) == 0, prevFailed)
	switch {
	case dryRun && (needGenerate || birthsPlanned > 0):
		if birthsPlanned > 0 && reason == "" {
			reason = "entity births would change the protos"
		}
		fmt.Printf("  📋 would run the generate pipeline (%s)\n", reason)
	case dryRun:
		fmt.Println("  📋 descriptor fresh — would skip")
	case needGenerate:
		fmt.Printf("  🔁 running the generate pipeline (%s)...\n", reason)
		if gerr := f.Gen.RunPipeline(root); gerr != nil {
			// The pipeline's own failure report (just above) repeated the
			// compiler output and preserved the reverted sources; point
			// the fix hint at that evidence when it exists so the user
			// debugs the preserved files instead of re-running blind.
			fix := "fix the generate failure, then re-run `forge scaffold`"
			if _, statErr := os.Stat(filepath.Join(root, ".forge", "failed-generate")); statErr == nil {
				fix = "inspect the failing generated sources preserved under .forge/failed-generate/ (compiler output repeated above and in its error.txt), fix the cause, then re-run `forge scaffold`"
			}
			return cliutil.WrapUserErr(ctxLabel, "generate pipeline (phase 2)", "", fix, gerr)
		}
		fmt.Println("  ✅ projection refreshed (descriptor, ORM, CRUD wiring, pb-through handler stubs)")
	default:
		fmt.Println("  ⏭️  descriptor fresh and nothing birthed — skipped")
	}

	// ── Summary ───────────────────────────────────────────────────────
	printProjectScaffoldSummary(summary, dryRun)
	// A birth that FAILED is work the project asked for and did not get.
	// One bad entity still never aborts the batch — every other entity is
	// born first — but the run itself must not report success, or a gate
	// running `forge scaffold` goes green over a missing entity.
	if len(summary.Failed) > 0 {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("%d entit%s could not be birthed (listed above)", len(summary.Failed), pluralY(len(summary.Failed))),
			"", "fix the reasons above and re-run `forge scaffold`")
	}
	return nil
}

// scaffoldServiceLeaves lists the proto/services/<leaf> directories in
// scope: all of them, or the one --service names (which must exist).
func scaffoldServiceLeaves(ctxLabel, root, svcFilter string) ([]string, error) {
	svcRoot := filepath.Join(root, "proto", "services")
	if svcFilter != "" {
		leaf := naming.ServicePackage(svcFilter)
		if fi, err := os.Stat(filepath.Join(svcRoot, leaf)); err != nil || !fi.IsDir() {
			return nil, cliutil.UserErr(ctxLabel,
				fmt.Sprintf("service %q has no proto directory at proto/services/%s", svcFilter, leaf), "",
				"name the service as its proto/services/ directory is spelled")
		}
		return []string{leaf}, nil
	}
	entries, err := os.ReadDir(svcRoot)
	if err != nil {
		return nil, nil // no protos yet — every phase no-ops with a report
	}
	var leaves []string
	for _, e := range entries {
		if e.IsDir() {
			leaves = append(leaves, e.Name())
		}
	}
	sort.Strings(leaves)
	return leaves, nil
}

// staleRawRPCs returns the rpc names the raw protos declare that the
// descriptor doesn't know — the staleness discriminator phase 2 shares
// with `scaffold rpc`'s self-heal.
func staleRawRPCs(scans map[string]*codegen.RawProtoScan, services []codegen.ServiceDef) []string {
	known := map[string]bool{}
	for _, sd := range services {
		for _, m := range sd.Methods {
			known[m.Name] = true
		}
	}
	var stale []string
	for _, scan := range scans {
		if scan == nil {
			continue
		}
		for _, r := range scan.RPCs {
			if !known[r.Name] {
				stale = append(stale, r.Name)
			}
		}
	}
	sort.Strings(stale)
	return stale
}

// generateReason names why phase 2 runs — causality stays legible.
func generateReason(birthsWrote bool, staleRPCs []string, noDescriptor, prevFailed bool) string {
	var parts []string
	if birthsWrote {
		parts = append(parts, "entity births changed the protos")
	}
	if len(staleRPCs) > 0 {
		parts = append(parts, fmt.Sprintf("descriptor stale: %s not in gen/forge_descriptor.json", strings.Join(staleRPCs, ", ")))
	}
	if noDescriptor {
		parts = append(parts, "no compiled descriptor yet")
	}
	if prevFailed {
		parts = append(parts, "previous generate failed and was rolled back (.forge/failed-generate/ present) — re-running the projection")
	}
	return strings.Join(parts, "; ")
}

// printProjectScaffoldSummary renders the one-table summary.
func printProjectScaffoldSummary(s *sweepSummary, dryRun bool) {
	fmt.Println("\n── Summary ───────────────────────────────────────────────────────")
	if dryRun {
		fmt.Println("  (dry run — nothing written)")
		return
	}
	list := func(items []string) string {
		if len(items) == 0 {
			return "0"
		}
		return fmt.Sprintf("%d (%s)", len(items), strings.Join(items, ", "))
	}
	fmt.Printf("  entities birthed:    %s\n", list(s.EntitiesBirthed))
	fmt.Printf("  managed fields:      %d declared on entity messages\n", s.ManagedFields)
	fmt.Printf("  quintets completed:  %d\n", s.QuintetsCompleted)
	fmt.Printf("  TODO fields carried: %d\n", s.TodoFields)
	fmt.Printf("  already born:        %d\n", len(s.Inert))
	// Refused is broken out from "skipped" on purpose: a refused marker is
	// an entity the project asked for and did not get. Rolled into one
	// skipped count it was indistinguishable from the inert markers every
	// steady-state re-run reports, and the run still ended "clean no-op".
	if len(s.Refused) == 0 {
		fmt.Printf("  refused:             0\n")
	} else {
		fmt.Printf("  refused:             %d\n", len(s.Refused))
		for _, r := range s.Refused {
			fmt.Printf("    ⏭️  %s\n", r)
		}
	}
	if len(s.Skipped) > 0 {
		fmt.Printf("  skipped:             %d\n", len(s.Skipped))
		for _, sk := range s.Skipped {
			fmt.Printf("    ⏭️  %s\n", sk)
		}
	}
	if len(s.Failed) > 0 {
		fmt.Printf("  failed:              %d\n", len(s.Failed))
		for _, fl := range s.Failed {
			fmt.Printf("    ⚠️  %s\n", fl)
		}
	}

	// The verdict must not contradict the lines above it.
	switch {
	case len(s.EntitiesBirthed) > 0:
		fmt.Println()
		fmt.Println("  Next: fill in the pb-through handler stubs (each returns an honest")
		fmt.Println("  Unimplemented sentinel) — everything is yours. Iterate with")
		fmt.Println("  `task test:short -- ./internal/handlers/...`; run `task test` before you finish.")
	case s.nothingHappened():
		fmt.Println("  ✅ nothing was missing — clean no-op")
	case len(s.Refused) > 0 || len(s.Failed) > 0:
		// Nothing born, and at least one marker the project wrote produced
		// no entity. State the split — "clean no-op" here was the sweep
		// reporting success for work it had just declined to do.
		fmt.Println()
		fmt.Printf("  ⚠️  nothing birthed this run: %d already born, %d refused, %d failed.\n",
			len(s.Inert), len(s.Refused), len(s.Failed))
		fmt.Println("  Address the reasons above (or drop the marker) and re-run `forge scaffold`.")
	default:
		// Only inert markers and/or benign per-item skips: the entities
		// already exist. Saying so beats "nothing was missing", which
		// reads as "forge found no markers at all".
		fmt.Printf("  ✅ nothing to birth — %d marked entit%s already born\n",
			len(s.Inert), pluralY(len(s.Inert)))
	}
}

// nothingHappened reports whether the sweep found no work of ANY kind — no
// birth, no inert marker, no refusal, no skip, no failure. That is the only
// state "nothing was missing" describes truthfully.
func (s *sweepSummary) nothingHappened() bool {
	return len(s.EntitiesBirthed) == 0 && len(s.Inert) == 0 &&
		len(s.Refused) == 0 && len(s.Skipped) == 0 && len(s.Failed) == 0
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// birthMigrationLine is the progress line for the migration an entity birth
// wrote: the file, project-relative. It used to end "(+down)", announcing a
// down migration nothing writes — forge rolls forward only — and print the
// absolute path, which put the machine's directory layout into every
// transcript of the README's first sixty seconds.
func birthMigrationLine(root, upPath string) string {
	shown := upPath
	if rel, err := filepath.Rel(root, upPath); err == nil && !strings.HasPrefix(rel, "..") {
		shown = filepath.ToSlash(rel)
	}
	return "     ✅ " + shown
}
