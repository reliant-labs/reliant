package cli

// `forge env floor` — rollback floors (floor.go; pkg/release floor.go).
//
// A floor is "never deploy anything older than X to this env". These verbs
// show, set, lower, clear and lift them; `forge env deploy --floor "<why>"`
// sets one at the release it deploys, and promoting a release that carries a
// rollback-floor declaration (a forge:breaks-rollback migration, a
// deploy/rollback-floors/<id>.yaml file) raises a declared floor on its own.
//
// Anyone who may promote may change floors (owner, 2026-11-08). Every change
// carries a reason and is recorded — on the ledger and in the activity stream
// — and there is no --force on deploy: changing the floor IS the escape.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/pkg/release"
)

// floorShowSchema names the `floor show --json` document.
const floorShowSchema = "forge.env.floor/v1"

// floorShowDocument is `forge env floor show --json`: the stable document the
// release train and agents read.
type floorShowDocument struct {
	Schema string `json:"schema"`
	Env    string `json:"env"`
	Ledger string `json:"ledger"`
	// Supported is false when the ledger cannot hold floors (a control
	// plane that predates them); Floors is then null — "unknown", which a
	// reader must not take for "none".
	Supported bool            `json:"supported"`
	Floors    []release.Floor `json:"floors"`
	Note      string          `json:"note,omitempty"`
}

// floorFlags are the flags every changing verb shares.
type floorFlags struct {
	reason string
	actor  string
	json   bool
}

func (f *floorFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.reason, "reason", "", "Why (required): recorded on the ledger and in the activity stream")
	cmd.Flags().StringVar(&f.actor, "actor", "", "Automation identity to record (e.g. ci, rc-train); default the current user")
	cmd.Flags().BoolVar(&f.json, "json", false, "Print one machine-readable document")
}

func (f *floorFlags) requireReason(verb string) error {
	if strings.TrimSpace(f.reason) == "" {
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf("forge env floor %s needs --reason \"<why>\": it is recorded with the change", verb)}
	}
	return nil
}

func newEnvFloorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "floor",
		Short: "Rollback floors: never deploy anything older than X to an environment",
		Long: `A rollback floor refuses every deploy of a release older than it to one environment.
Two kinds:

  order      the target must not be cut before the floor's release
             (set by hand, or by deploy --floor; "roll forward only from here")
  declared   the target must carry every declaration the floor requires: raised
             automatically when the env is promoted to a release carrying a
             forge:breaks-rollback migration or a deploy/rollback-floors/<id>.yaml

  show  <env> [--all]                         the env's floors (--all: lifted ones too)
  check <env> <release>                       would they admit <release>? (reads only; exit 0/1/2)
  set   <env> (<release> | --current) --reason  an order floor
  lower <env> <release> --reason              lift every order floor above <release>, set one at it
  clear <env> --reason                        lift every order floor (declared floors are kept)
  lift  <env> <floor-id>... --reason [--acknowledge <source>:<id>]...
                                              lift floors by id; a declared floor needs every
                                              declaration it requires acknowledged

A deploy below a floor is refused (exit 1) before anything is built. Anyone who
may promote may change floors; every change carries its reason and principal on
the ledger and in the activity stream.`,
	}
	cmd.AddCommand(newEnvFloorShowCmd(), newEnvFloorCheckCmd(), newEnvFloorSetCmd(), newEnvFloorLowerCmd(), newEnvFloorClearCmd(), newEnvFloorLiftCmd())
	return cmdutil.StrictGroup(cmd)
}

// floorCheckDocument is `forge env floor check --json`.
type floorCheckDocument struct {
	Schema  string `json:"schema"`
	Env     string `json:"env"`
	Release string `json:"release"`
	// Verdict is admitted | below_floor | unknown | unsupported.
	Verdict string               `json:"verdict"`
	Floor   *promoteFloorVerdict `json:"floor,omitempty"`
	Refusal *promoteRefusalJSON  `json:"refusal,omitempty"`
	Detail  string               `json:"detail,omitempty"`
}

func newEnvFloorCheckCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "check <env> <release>",
		Short: "Would <env>'s rollback floors admit <release>? Reads only",
		Long: `The check ` + "`forge env deploy <env> <release>`" + ` makes before its plan, alone: whether
the env's rollback floors admit the release (with git, an order floor also
refuses a release whose commit is an ancestor of the floor's). Nothing is
built, rendered or written — a release pipeline asks it before it tags.

Exit 0 admitted, 1 below a floor (below_rollback_floor), 2 could not tell (the
ledger or the release could not be read, or the ledger holds no floors).`,
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFloorCheck(cmd.Context(), cmd.OutOrStdout(), args[0], args[1], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print one machine-readable document (forge.env.floor-check/v1)")
	return cmd
}

func runFloorCheck(ctx context.Context, out io.Writer, env, version string, jsonOut bool) error {
	doc := floorCheckDocument{Schema: "forge.env.floor-check/v1", Env: env, Release: version}
	projectDir := projectDirForKCL()
	ledger, err := ledgerFor(ctx, projectDir, env)
	var result error
	if err != nil {
		doc.Verdict, doc.Detail = "unknown", err.Error()
		result = undeterminedf("choose the ledger of %s: %v", env, err)
	} else {
		verdict, target, err := checkDeployFloors(ctx, io.Discard, projectDir, env, version, ledger, nil, "")
		var refused *promoteRefusedError
		switch {
		case errors.As(err, &refused):
			doc.Verdict, doc.Refusal, doc.Detail, result = "below_floor", refused.toJSON(), refused.Error(), refused
		case err != nil:
			doc.Verdict, doc.Detail, result = "unknown", err.Error(), undeterminedf("%v", err)
		case target == nil || verdict.Verdict == "unknown" || verdict.Verdict == "unsupported":
			doc.Verdict, doc.Floor, doc.Detail = verdict.Verdict, verdict, verdict.Note
			result = undeterminedf("%s's rollback floors could not judge %s: %s", env, version, verdict.Note)
		default:
			doc.Verdict, doc.Floor = "admitted", verdict
		}
	}
	if jsonOut {
		if err := emitJSONDocument(doc); err != nil {
			return err
		}
		return result
	}
	switch doc.Verdict {
	case "admitted":
		fmt.Fprintf(out, "%s's rollback floors admit %s.\n", env, version)
		renderFloorVerdict(out, doc.Floor)
	}
	return result
}

// floorStoreFor resolves env's ledger and its floor seam.
func floorStoreFor(ctx context.Context, env string) (floorStore, envLedger, error) {
	ledger, err := ledgerFor(ctx, projectDirForKCL(), env)
	if err != nil {
		return nil, envLedger{}, undeterminedf("choose the ledger of %s: %v", env, err)
	}
	store, ok := ledger.Bindings.(floorStore)
	if !ok {
		return nil, ledger, exitCodeError{code: exitWrong, msg: fmt.Sprintf("the ledger at %s holds no rollback floors", ledger.Bindings.Location())}
	}
	return store, ledger, nil
}

func newEnvFloorShowCmd() *cobra.Command {
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:          "show <env>",
		Short:        "Show an environment's rollback floors",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFloorShow(cmd.Context(), cmd.OutOrStdout(), args[0], all, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include lifted floors, with their lifts")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print one machine-readable document ("+floorShowSchema+")")
	return cmd
}

func runFloorShow(ctx context.Context, out io.Writer, env string, all, jsonOut bool) error {
	store, ledger, err := floorStoreFor(ctx, env)
	doc := floorShowDocument{Schema: floorShowSchema, Env: env}
	if ledger.Bindings != nil {
		doc.Ledger = ledger.Bindings.Location()
	}
	if err == nil {
		doc.Floors, err = store.RollbackFloors(ctx, env, all)
	}
	switch {
	case isFloorsUnsupported(err):
		doc.Note = err.Error()
	case err != nil:
		return err
	default:
		doc.Supported = true
		if doc.Floors == nil {
			doc.Floors = []release.Floor{}
		}
	}
	if jsonOut {
		return emitJSONDocument(doc)
	}
	renderFloors(out, doc)
	return nil
}

func renderFloors(out io.Writer, doc floorShowDocument) {
	if !doc.Supported {
		fmt.Fprintf(out, "%s: rollback floors unknown — %s\n", doc.Env, doc.Note)
		return
	}
	if len(doc.Floors) == 0 {
		fmt.Fprintf(out, "%s has no rollback floor: any release can be deployed to it.\n", doc.Env)
		return
	}
	fmt.Fprintf(out, "%s's rollback floors (%s):\n", doc.Env, doc.Ledger)
	for _, f := range doc.Floors {
		renderFloorLine(out, f)
	}
}

// renderFloorLine is one floor in `floor show` and `env status`.
func renderFloorLine(out io.Writer, f release.Floor) {
	state := ""
	if !f.Active() {
		state = " [LIFTED]"
	}
	by := floorActor(f.SetBy)
	switch f.Kind {
	case release.FloorDeclared:
		refs := make([]string, 0, len(f.Requires))
		for _, d := range f.Requires {
			refs = append(refs, fmt.Sprintf("%s %s", d.Kind, d.ID))
		}
		fmt.Fprintf(out, "  %s%s  declared: must carry %s (since %s, %s%s)\n",
			f.ID, state, strings.Join(refs, ", "), f.Release, formatFloorTime(f.SetAt), by)
		for _, d := range f.Requires {
			if d.Reason != "" {
				fmt.Fprintf(out, "      %s: %s\n", d.ID, d.Reason)
			}
		}
	default:
		fmt.Fprintf(out, "  %s%s  order: nothing cut before %s — %s, %q (%s%s)\n",
			f.ID, state, f.Release, f.Source, f.Reason, formatFloorTime(f.SetAt), by)
	}
	if f.Lift != nil {
		fmt.Fprintf(out, "      lifted %s%s: %q\n", formatFloorTime(f.Lift.At), floorActor(f.Lift.By), f.Lift.Reason)
	}
}

func floorActor(a release.Actor) string {
	switch {
	case a.Actor != "" && a.User != "":
		return fmt.Sprintf(", by %s (%s)", a.User, a.Actor)
	case a.Actor != "":
		return ", by " + a.Actor
	case a.User != "":
		return ", by " + a.User
	}
	return ""
}

func formatFloorTime(t time.Time) string {
	if t.IsZero() {
		return "unknown time"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

func newEnvFloorSetCmd() *cobra.Command {
	var (
		f       floorFlags
		current bool
		expect  string
	)
	cmd := &cobra.Command{
		Use:          "set <env> (<release> | --current)",
		Short:        "Set an order floor: nothing cut before <release> deploys here",
		Args:         cobra.RangeArgs(1, 2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if (len(args) == 2) == current {
				return exitCodeError{code: exitWrong, msg: "name exactly one of a release or --current"}
			}
			if err := f.requireReason("set"); err != nil {
				return err
			}
			ctx := cmd.Context()
			store, ledger, err := floorStoreFor(ctx, args[0])
			if err != nil {
				return err
			}
			req := floorSetRequest{Current: current, Reason: f.reason, By: promoteActor(f.actor)}
			if len(args) == 2 {
				req.Version = args[1]
			}
			// --current is "at what I see": the floor lands only if the env
			// is still on the promotion read here.
			if current {
				req.Guard, err = floorGuard(ctx, ledger, args[0], expect)
				if err != nil {
					return err
				}
			}
			floor, err := store.SetRollbackFloor(ctx, args[0], req)
			if err != nil {
				return err
			}
			if f.json {
				return emitJSONDocument(floor)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Set a rollback floor on %s:\n", args[0])
			renderFloorLine(cmd.OutOrStdout(), floor)
			return nil
		},
	}
	f.bind(cmd)
	cmd.Flags().BoolVar(&current, "current", false, "At the release the env runs now (roll forward only from here)")
	cmd.Flags().StringVar(&expect, "expect-current", "", "With --current: the promotion id the env must still be on (default: the one read now); exit 3 if it moved")
	return cmd
}

// floorGuard is the compare-and-set for `floor set --current`: the promotion
// the command read, or the one --expect-current names.
func floorGuard(ctx context.Context, ledger envLedger, env, expect string) (appendGuard, error) {
	switch {
	case expect == expectUnboundLiteral:
		return appendGuard{ExpectUnbound: true}, nil
	case expect != "":
		return appendGuard{ExpectedCurrentID: expect}, nil
	}
	current, bound, err := ledger.Bindings.Current(ctx, env)
	if err != nil {
		return appendGuard{}, undeterminedf("read %s's current promotion: %v", env, err)
	}
	if !bound {
		return appendGuard{ExpectUnbound: true}, nil
	}
	return appendGuard{ExpectedCurrentID: current.ID}, nil
}

func newEnvFloorLowerCmd() *cobra.Command {
	var f floorFlags
	cmd := &cobra.Command{
		Use:          "lower <env> <release>",
		Short:        "Lift every order floor above <release> and set one at it, in one change",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.requireReason("lower"); err != nil {
				return err
			}
			return runFloorLift(cmd.Context(), cmd.OutOrStdout(), args[0], floorLiftRequest{LowerTo: args[1], Reason: f.reason, By: promoteActor(f.actor)}, f.json)
		},
	}
	f.bind(cmd)
	return cmd
}

func newEnvFloorClearCmd() *cobra.Command {
	var f floorFlags
	cmd := &cobra.Command{
		Use:          "clear <env>",
		Short:        "Lift every order floor (declared floors are kept; lift them by id)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.requireReason("clear"); err != nil {
				return err
			}
			return runFloorLift(cmd.Context(), cmd.OutOrStdout(), args[0], floorLiftRequest{Clear: true, Reason: f.reason, By: promoteActor(f.actor)}, f.json)
		},
	}
	f.bind(cmd)
	return cmd
}

func newEnvFloorLiftCmd() *cobra.Command {
	var (
		f    floorFlags
		acks []string
	)
	cmd := &cobra.Command{
		Use:   "lift <env> <floor-id>...",
		Short: "Lift floors by id; a declared floor needs each declaration it requires acknowledged",
		Long: `Lift rollback floors by id (forge env floor show <env> lists them).

A DECLARED floor states that the env's schema, API or data moved past what older
releases can run on. Lifting one lets those releases back in, so the lift must
name every declaration the floor requires with --acknowledge <source>:<id>,
vouching that it does not apply — usually because the rollout that raised the
floor never ran its migrator. The lift, its reason and the acknowledgements are
recorded.`,
		Args:         cobra.MinimumNArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.requireReason("lift"); err != nil {
				return err
			}
			req := floorLiftRequest{FloorIDs: args[1:], Reason: f.reason, By: promoteActor(f.actor)}
			for _, a := range acks {
				ref, err := release.ParseDeclarationRef(a)
				if err != nil {
					return exitCodeError{code: exitWrong, msg: err.Error()}
				}
				req.Acknowledged = append(req.Acknowledged, ref)
			}
			return runFloorLift(cmd.Context(), cmd.OutOrStdout(), args[0], req, f.json)
		},
	}
	f.bind(cmd)
	cmd.Flags().StringArrayVar(&acks, "acknowledge", nil, "A declaration this lift vouches for, as <source>:<id> (repeat for each a declared floor requires)")
	return cmd
}

func runFloorLift(ctx context.Context, out io.Writer, env string, req floorLiftRequest, jsonOut bool) error {
	store, _, err := floorStoreFor(ctx, env)
	if err != nil {
		return err
	}
	if missing := missingAcknowledgements(ctx, store, env, req); len(missing) > 0 {
		return exitCodeError{code: exitWrong, msg: fmt.Sprintf(
			"a declared floor lifts only with each declaration it requires acknowledged; add:\n  %s", strings.Join(missing, "\n  "))}
	}
	res, err := store.LiftRollbackFloors(ctx, env, req)
	if err != nil {
		return err
	}
	if jsonOut {
		return emitJSONDocument(res)
	}
	if len(res.Lifted) == 0 && res.Set == nil {
		fmt.Fprintf(out, "%s has no order floor to lift; nothing changed.\n", env)
		return nil
	}
	for _, f := range res.Lifted {
		fmt.Fprintf(out, "Lifted on %s:\n", env)
		renderFloorLine(out, f)
	}
	if res.Set != nil {
		fmt.Fprintf(out, "Set on %s:\n", env)
		renderFloorLine(out, *res.Set)
	}
	return nil
}

// missingAcknowledgements names, as the flags to add, every declaration a
// named declared floor requires that the request does not acknowledge: the
// fix printed with the refusal rather than after a round trip. Best-effort —
// the ledger refuses the lift regardless.
func missingAcknowledgements(ctx context.Context, store floorStore, env string, req floorLiftRequest) []string {
	if len(req.FloorIDs) == 0 {
		return nil
	}
	floors, err := store.RollbackFloors(ctx, env, false)
	if err != nil {
		return nil
	}
	named := map[string]bool{}
	for _, id := range req.FloorIDs {
		named[id] = true
	}
	acked := map[release.DeclarationRef]bool{}
	for _, a := range req.Acknowledged {
		acked[a] = true
	}
	var out []string
	for _, f := range floors {
		if !named[f.ID] || f.Kind != release.FloorDeclared {
			continue
		}
		for _, d := range f.Requires {
			if !acked[d.Ref()] {
				out = append(out, "--acknowledge "+d.Ref().String())
			}
		}
	}
	return out
}
