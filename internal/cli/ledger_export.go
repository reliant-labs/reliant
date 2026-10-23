package cli

// `forge ledger export --dir D` — write the selected ledger out in
// file-ledger format.
//
// IT EXISTS ONLY TO VERIFY AN IMPORT (owner decision O-1, doc §7.2, §11.1
// step 7). The sequence it serves is exactly one: import a git ledger, export
// it to a throwaway directory, and diff that against the `.forge/{releases,
// promotions}` files it came from. If the two agree, the import did not lose
// or reorder anything — which is the only way to know that before the source
// files are deleted (§11.1 step 9).
//
// WHAT IT IS NOT, stated here because the file's mere existence invites the
// wrong reading:
//
//   - It is NOT a backup, and must not be documented, scheduled or scaffolded
//     as one. A second copy of deploy history that nobody writes to goes stale
//     the moment the next promotion lands, and a stale ledger that LOOKS
//     authoritative is worse than none — it is the exact failure the machine
//     ledger was moved out of the checkout to end.
//   - There is NO `--git-ref` and no git export. O-1 removed the git side in
//     both directions: from this point a project's deploy history lives in the
//     hosted ledger or the machine ledger, and in no commit.
//   - It is not a migration path BETWEEN stores. That is `ledger import
//     --from-file-ledger`, which reads the same layout and goes through the
//     import's rules rather than writing files past them.
//
// Hence `--dir` is required with no default: there is no blessed location,
// because a blessed location is a thing people start to rely on.
//
// BYTE-EQUAL MODULO DOCUMENTED NORMALIZATIONS. The export writes the store's
// own canonical JSON, which is what makes the round trip meaningful rather
// than a re-rendering: see exportNormalizations for the four differences
// against a retired git ledger, each of which is a fact about the SOURCE
// format rather than a lossy step in the import.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// exportNormalizations are the ways an export of an imported ledger differs
// from the git files it came from. Printed with every export, because a
// reader diffing the two needs to know which differences are expected before
// they can tell that the rest are not.
//
// Every one of them is a property of the RETIRED format, not a loss:
var exportNormalizations = []string{
	"releases are one releases.jsonl, not one file per version (the retired v1_0_0.json did not name its own version)",
	"keys are in canonical order, and absent optional fields are omitted rather than written empty",
	"each imported promotion carries an extra imported_from naming the git path and blob it came from",
	"a promotion's id and promoted_at are preserved verbatim, so the ORDER of each env's log is unchanged",
}

// newLedgerExportCmd is `forge ledger export`.
func newLedgerExportCmd() *cobra.Command {
	var (
		dir    string
		envArg string
	)

	cmd := &cobra.Command{
		Use:   "export --dir <directory>",
		Short: "Write the selected ledger to a directory, to VERIFY an import",
		Args:  cobra.NoArgs,
		Long: `Write a ledger out in file-ledger format, so an import can be checked against
the files it came from.

THIS IS A VERIFICATION AID, NOT A BACKUP. It exists for one sequence:

  ` + Name() + ` ledger import --from-git --rev origin/main --apply
  ` + Name() + ` ledger export --dir /tmp/verify
  diff -r /tmp/verify/promotions .forge/promotions

If those agree, the import lost nothing — which is what you need to know
before the committed ledger is deleted from the repository.

It is NOT a way to keep a second copy of deploy history. A copy nothing writes
to is stale as soon as the next promotion lands, and a stale ledger that looks
authoritative is the failure the ledger was moved out of the checkout to end.
There is no git export in either direction, and no default directory — a
default is a thing people start to rely on.

The output is the ledger's own canonical JSON, so a diff is meaningful rather
than a re-rendering. Four differences against a retired git ledger are
expected and are printed with every run.

Examples:
  ` + Name() + ` ledger export --dir /tmp/verify
  ` + Name() + ` ledger export --dir /tmp/verify --env prod   # the ledger prod selected`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLedgerExport(cmd, dir, envArg)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "The directory to write to (required; use a throwaway path)")
	cmd.Flags().StringVar(&envArg, "env", "", "Export the ledger this environment selected (default: this machine's ledger for the project)")
	return cmd
}

// runLedgerExport writes the ledger to dir.
func runLedgerExport(cmd *cobra.Command, dir, envArg string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("--dir is required: name a throwaway directory to write the ledger to.\n" +
			"  There is no default, because this is a verification aid rather than a backup location")
	}
	projectDir := projectDirForKCL()

	store, err := exportStoreFor(cmd.Context(), projectDir, envArg)
	if err != nil {
		return err
	}
	counts, err := exportLedger(store, dir)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Wrote %s to %s: %s.\n", store.Dir(), dir, describeImportCounts(counts))
	fmt.Fprintf(out, "\nExpected differences against a retired .forge/ ledger:\n")
	for _, note := range exportNormalizations {
		fmt.Fprintf(out, "  - %s\n", note)
	}
	fmt.Fprintf(out, "\nThis directory is for one diff. It is not a backup: nothing writes to it again.\n")
	return nil
}

// exportStoreFor resolves which ledger to export.
//
// ONLY THE MACHINE LEDGER CAN BE EXPORTED, and that is a real limit rather
// than an unfinished one. The export writes the FILE layout, and a control
// plane's ledger is not a set of files — rendering one as files would produce
// exactly the stale-looking-authoritative artifact O-1 removed, and it would
// have to invent the per-record provenance the hosted rows hold in columns.
// An env on a control plane is verified by reading it back through that
// control plane, which is what `forge env status` does.
func exportStoreFor(ctx context.Context, projectDir, envArg string) (*ledgerfile.Store, error) {
	if envArg != "" {
		l, err := selectLedger(ctx, projectDir, envArg)
		if err != nil {
			return nil, err
		}
		if err := refuseHostedExport(envArg, l); err != nil {
			return nil, err
		}
	}
	return openMachineLedger(projectDir)
}

// refuseHostedExport is the hosted refusal, split out from the selection so
// it is testable from a literal envLedger — selectLedger renders KCL, and a
// test of this message should not need a project that declares a control
// plane.
func refuseHostedExport(env string, l envLedger) error {
	if !l.Hosted {
		return nil
	}
	return fmt.Errorf("env %q keeps its ledger on the control plane at %s, which has no file form to export.\n"+
		"  An export writes the FILE layout; a hosted ledger is rows, and rendering them as files would produce "+
		"a second copy nothing maintains.\n"+
		"  fix: read it back with `%s env status %s` instead",
		env, l.Bindings.Location(), Name(), env)
}

// exportLedger writes every record the store holds into dir, in the §12.2
// layout.
//
// The LOCK IS NOT HELD across the whole export, and that is deliberate. Each
// read takes it, so no read sees a torn line, but a promotion landing
// mid-export would appear in one file and not another. For the one job this
// command has — diffing an import that has already finished against static
// git files — that is irrelevant, and holding the lock for the duration would
// block a concurrent deploy for the sake of a consistency nobody reads.
func exportLedger(store *ledgerfile.Store, dir string) (map[string]int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	counts := map[string]int{}

	releases, err := store.Releases()
	if err != nil {
		return nil, err
	}
	if err := writeJSONLines(filepath.Join(dir, "releases.jsonl"), toAny(releases)); err != nil {
		return nil, err
	}
	counts["releases"] = len(releases)

	envs, err := store.Envs()
	if err != nil {
		return nil, err
	}
	if err := writeJSONLines(filepath.Join(dir, "envs.jsonl"), toAny(envs)); err != nil {
		return nil, err
	}
	counts["environments"] = len(envs)

	// Every env with a promotion log, not just the declared ones: a
	// RETIRED env's history is part of what an import brought, and an
	// export that skipped it would make the import look lossy against the
	// very files it read (control-plane's staging and preprod, O-9).
	for _, env := range exportEnvNames(store, envs) {
		promotions, err := store.Promotions(env)
		if err != nil {
			return nil, err
		}
		if len(promotions) == 0 {
			continue
		}
		// The imported_from each line carries is part of the record as
		// this store holds it, so the export preserves it rather than
		// re-encoding from release.Promotion and dropping it.
		from, err := store.ImportedFrom(env)
		if err != nil {
			return nil, err
		}
		lines := make([]any, 0, len(promotions))
		for _, p := range promotions {
			lines = append(lines, exportedPromotionOf(p, from[p.ID]))
		}
		if err := writeJSONLines(filepath.Join(dir, "promotions", env+".jsonl"), lines); err != nil {
			return nil, err
		}
		counts["promotions"] += len(promotions)

		applies, err := store.Applies(env)
		if err != nil {
			return nil, err
		}
		if len(applies) > 0 {
			lines := make([]any, 0, len(applies))
			for _, a := range applies {
				lines = append(lines, exportedApply{Apply: a.Apply, Outcome: a.Outcome})
			}
			if err := writeJSONLines(filepath.Join(dir, "applies", env+".jsonl"), lines); err != nil {
				return nil, err
			}
			counts["applies"] += len(applies)
		}
	}

	bundles, err := store.Bundles("")
	if err != nil {
		return nil, err
	}
	if len(bundles) > 0 {
		if err := writeJSONLines(filepath.Join(dir, "bundles.jsonl"), toAny(bundles)); err != nil {
			return nil, err
		}
		counts["bundles"] = len(bundles)
	}

	sessions, err := store.Sessions()
	if err != nil {
		return nil, err
	}
	if len(sessions) > 0 {
		if err := writeJSONLines(filepath.Join(dir, "sessions.jsonl"), toAny(sessions)); err != nil {
			return nil, err
		}
		counts["sessions"] = len(sessions)
	}
	return counts, nil
}

// exportEnvNames is every env the ledger holds records for: the declared
// ones, plus any with a promotion log on disk.
func exportEnvNames(store *ledgerfile.Store, envs []release.EnvRecord) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range envs {
		if !seen[e.Name] {
			seen[e.Name] = true
			out = append(out, e.Name)
		}
	}
	// The logs themselves are the authority for "which envs have
	// history": an imported env whose record was never written still has
	// its promotions, and an export that missed them would read as a
	// lossy import.
	for _, name := range promotionLogEnvs(store) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// promotionLogEnvs is every env with a promotion log on disk.
//
// It reads the DIRECTORY rather than the env records, because the two can
// legitimately disagree: an imported env whose record was never written
// still has its promotions, and a retired env (control-plane's staging and
// preprod, O-9) has history and no declaration anywhere. A reader that
// enumerated only declared envs would report that history as absent.
//
// A glob failure is an empty list, not an error. This is one half of an
// answer that already has another source, and refusing to report anything
// because a directory could not be listed would be worse than reporting the
// declared envs alone.
func promotionLogEnvs(store *ledgerfile.Store) []string {
	logs, err := filepath.Glob(filepath.Join(store.Dir(), "promotions", "*.jsonl"))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(logs))
	for _, path := range logs {
		out = append(out, strings.TrimSuffix(filepath.Base(path), ".jsonl"))
	}
	sort.Strings(out)
	return out
}

// exportedPromotion is a promotion plus the provenance key the store's line
// carries, flattened the same way — so an exported line is byte-identical to
// the stored one.
//
// It marshals through a map for the reason ledgerfile's own importer does:
// re-encoding from a parallel struct would be a second encoding of
// release.Promotion, free to drift from the first.
type exportedPromotion struct {
	promotion    release.Promotion
	importedFrom string
}

func exportedPromotionOf(p release.Promotion, from string) exportedPromotion {
	return exportedPromotion{promotion: p, importedFrom: from}
}

func (e exportedPromotion) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(e.promotion)
	if err != nil {
		return nil, err
	}
	if e.importedFrom == "" {
		return encoded, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	marker, err := json.Marshal(e.importedFrom)
	if err != nil {
		return nil, err
	}
	fields["imported_from"] = marker
	return json.Marshal(fields)
}

// exportedApply is one apply joined to its outcome, in the store's own line
// shape.
type exportedApply struct {
	Apply   release.Apply         `json:"apply"`
	Outcome *release.ApplyOutcome `json:"outcome,omitempty"`
}

// writeJSONLines writes one record per line, creating parent directories. A
// file with no records is still CREATED, empty: "this env has no applies" and
// "this export did not look at applies" must not render alike, and an absent
// file cannot tell them apart.
func writeJSONLines(path string, records []any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	var buf strings.Builder
	for _, r := range records {
		line, err := exportCanonicalJSON(r)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := atomicfile.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// exportCanonicalJSON matches internal/ledgerfile's encoder exactly —
// SetEscapeHTML(false), no indentation — which is what makes an exported line
// byte-identical to the stored one rather than merely equivalent. A diff is
// the whole point of this command, so an encoder that escaped `&`
// differently would produce spurious differences on every promotion note
// containing one.
func exportCanonicalJSON(v any) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode ledger record: %w", err)
	}
	return []byte(strings.TrimRight(buf.String(), "\n")), nil
}

// toAny widens a typed slice for writeJSONLines.
func toAny[T any](in []T) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}
