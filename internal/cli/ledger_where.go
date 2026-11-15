package cli

// `forge ledger where <env>` — which backend holds this env's ledger, and
// WHY.
//
// The "why" is the point, and it is why this is a command rather than a line
// in `env status`. Ledger selection is declarative and invisible: an env
// whose KCL declares `forge.ControlPlane` records there, every other env
// records in this machine's store, and nothing in a normal command's output
// says which happened. So the two questions a user actually has when a
// promotion is "missing" — where did it go, and what decided that — have no
// answer anywhere.
//
// It answers them by rendering the env and reporting the DECLARATION it
// found, not by guessing from what exists. A ledger that happens to hold
// records is not evidence it is the selected one: a project that moved an
// env to a control plane still has the old machine-ledger files sitting
// there, and "where are the records" and "where do records GO" are different
// questions with, briefly, different answers.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// ledgerBackendKind is which store an env selected. Closed, and spelled the
// same in --json as in the text output.
type ledgerBackendKind string

const (
	// ledgerBackendControlPlane: the env declares forge.ControlPlane.
	ledgerBackendControlPlane ledgerBackendKind = "control_plane"
	// ledgerBackendMachine: this machine's store under $FORGE_LEDGER_HOME.
	ledgerBackendMachine ledgerBackendKind = "machine"
)

// ledgerWhereDoc is one env's answer.
type ledgerWhereDoc struct {
	Env     string            `json:"env"`
	Backend ledgerBackendKind `json:"backend"`
	// Location is the endpoint URL or the ledger directory. Opaque:
	// callers PRINT it.
	Location string `json:"location"`
	// Because is the declaration that chose this backend, in one line.
	Because string `json:"because"`
	// Declaration is the control-plane declaration when there is one, so a
	// reader can see the endpoint and organization the KCL named rather
	// than inferring them from the URL. Present under an override too: it
	// is then the declaration that was NOT used.
	Declaration *ledgerDeclarationDoc `json:"declaration,omitempty"`
	// Override is "FORGE_LEDGER=machine" when that variable displaced a
	// declared control plane, so a script can assert its run stays local.
	Override string `json:"override,omitempty"`
	// Project is the forge project the ledger is keyed by. It is what
	// makes two worktrees of one project share one machine ledger, and it
	// scopes a hosted env's identity, so a reader diagnosing "why is this
	// ledger empty" needs it either way.
	Project string `json:"project"`
}

// ledgerDeclarationDoc is the KCL declaration that selected a control plane.
type ledgerDeclarationDoc struct {
	Endpoint string `json:"endpoint"`
	// TokenEnv is the env var the credential is read from, when the
	// declaration names one. The VALUE is never printed.
	TokenEnv string `json:"token_env,omitempty"`
}

// newLedgerWhereCmd is `forge ledger where <env>`.
func newLedgerWhereCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "where <environment>",
		Short: "Print which backend holds an environment's ledger, and the declaration that chose it",
		Args:  cobra.ExactArgs(1),
		Long: `Say where an environment's promotions and releases are recorded, and why.

Selection is DECLARATIVE and has no flag: an environment whose KCL declares
` + "`forge.ControlPlane`" + ` records on that control plane — whatever its kind,
including local — and every other environment records in this machine's
ledger under $FORGE_LEDGER_HOME (default ~/.forge/ledger), keyed by project so
that every worktree of a project shares one history.

ONE OVERRIDE: FORGE_LEDGER=machine sends every environment to this machine's
ledger, whatever it declares, and nothing is read from or written to the
declared control plane. It is for hermetic tests and scripts that render or
import a real environment. $FORGE_LEDGER_HOME alone does NOT do this: it says
WHERE the machine ledger is, not WHETHER an environment uses it. A script can
assert its run stays local with ` + "`" + `ledger where <env> --json` + "`" + ` (backend
"machine", override "FORGE_LEDGER=machine").

This renders the environment and reports the declaration it FOUND. It does not
infer the answer from which store happens to hold records: a project that has
moved an environment to a control plane still has its old machine-ledger files
on disk, and "where are the records" is a different question from "where do
records go".

Examples:
  ` + Name() + ` ledger where prod
  ` + Name() + ` ledger where dev --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ledgerWhereFor(cmd.Context(), projectDirForKCL(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			writeLedgerWhere(cmd.OutOrStdout(), doc)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print {env, backend, location, because, declaration, override, project} as JSON")
	return cmd
}

// ledgerWhereFor resolves one env's backend and the reason for it.
//
// It renders through selectLedger rather than ledgerFor, for the same reason
// the import does: ledgerFor runs the unimported-checkout refusal, and
// refusing to SAY where a ledger is because that ledger is missing history is
// exactly backwards — this is one of the commands a user runs to understand
// that refusal.
func ledgerWhereFor(ctx context.Context, projectDir, env string) (ledgerWhereDoc, error) {
	l, err := selectLedger(ctx, projectDir, env)
	if err != nil {
		return ledgerWhereDoc{}, err
	}
	doc := ledgerWhereDoc{
		Env:      env,
		Location: l.Bindings.Location(),
		Project:  hostedProjectName(),
	}
	if !l.Hosted && l.OverriddenEndpoint != "" {
		// The env DOES declare a control plane; FORGE_LEDGER kept this
		// run off it. Reported as an override, with the declaration it
		// overrode, so it cannot be mistaken for an env declaring nothing.
		doc.Backend = ledgerBackendMachine
		doc.Override = ledgerSelectionEnv + "=" + string(ledgerSelectMachine)
		doc.Because = doc.Override + " overrides the forge.ControlPlane deploy/kcl/" + env +
			"/ declares, so this run records in this machine's store and reads nothing from that control plane"
		doc.Declaration = &ledgerDeclarationDoc{Endpoint: l.OverriddenEndpoint}
		return doc, nil
	}
	if !l.Hosted {
		doc.Backend = ledgerBackendMachine
		doc.Because = "this environment declares no forge.ControlPlane, so its ledger is this machine's store, keyed by project"
		return doc, nil
	}
	doc.Backend = ledgerBackendControlPlane
	doc.Because = "deploy/kcl/" + env + "/ declares forge.ControlPlane, so this environment's ledger is that control plane"
	// Re-read the declaration for the reason line. A second render is
	// avoided by reading the entities once here; the endpoint on the
	// ledger is already resolved, but the DECLARATION is what the user
	// edits and therefore what the answer has to name.
	if entities, rerr := RenderKCL(ctx, projectDir, env); rerr == nil {
		if decl := declarationFromEntities(entities); decl != nil {
			doc.Declaration = &ledgerDeclarationDoc{
				Endpoint: decl.Endpoint,
				TokenEnv: decl.TokenEnv,
			}
		}
	}
	if l.Mixed {
		doc.Because += ". It also declares workloads that control plane does not run, so forge applies that part itself"
	}
	return doc, nil
}

// writeLedgerWhere is the human form: the answer, then the reason.
func writeLedgerWhere(w io.Writer, doc ledgerWhereDoc) {
	fmt.Fprintf(w, "env %s records in %s\n", doc.Env, doc.Location)
	fmt.Fprintf(w, "  backend: %s\n", doc.Backend)
	fmt.Fprintf(w, "  because: %s\n", doc.Because)
	if doc.Override != "" {
		fmt.Fprintf(w, "  override: %s\n", doc.Override)
	}
	if d := doc.Declaration; d != nil {
		fmt.Fprintf(w, "  declared endpoint: %s\n", d.Endpoint)
		if d.TokenEnv != "" {
			// The NAME, never the value. A command that printed a
			// credential would make its own output unsafe to paste
			// into a bug report, which is where this output goes.
			fmt.Fprintf(w, "  credential from:   $%s\n", d.TokenEnv)
		}
	}
	fmt.Fprintf(w, "  project: %s\n", emptyAs(doc.Project, "(unnamed — see forge.yaml)"))
}
