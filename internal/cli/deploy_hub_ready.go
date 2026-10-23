package cli

// A hub-converged env is applied by the control plane's reconciler, one
// Kustomization per connected cluster. Recording a promotion for it is only
// worth doing if that reconciler can apply anything at all.
//
// WHY THIS CHECK EXISTS. forge decides "the hub converges this env" from the
// declaration alone (envConvergedByHub). On 2026-10-07 prod declared its
// connected clusters, the hub created the OCIRepository, and both control-plane
// Kustomizations were Ready=False: Forbidden — the impersonated per-org service
// account had no rights in the cluster. A deploy would have recorded a
// promotion and then waited out its whole budget to an UNKNOWN, with nothing
// connecting the timeout to the hub's own error. The hub had already said what
// was wrong; forge just never asked.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// procListConvergencesCLI is controlplane.v1.DeployService/ListConvergences.
const procListConvergencesCLI = "controlplane.v1.DeployService/ListConvergences"

// hubReading is the hub's newest reconcile observation for one cluster.
type hubReading struct {
	Cluster     string `json:"cluster"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
	Message     string `json:"message"`
	PromotionID string `json:"promotionId"`
	// Revision is what the reconciler reported APPLIED, verbatim.
	Revision string `json:"revision,omitempty"`
	// Tree is the bundle tree this reading is about (its path), when the
	// control plane reports per tree. Empty on a control plane that reports one
	// reading per cluster, which is every one that predates control-plane C2:
	// the display then says the reading is cluster-level instead of claiming a
	// precision it does not have.
	Tree       string `json:"tree,omitempty"`
	ObservedAt string `json:"observedAt,omitempty"`
}

// readHubReadings asks the env's control plane for its newest reconcile
// observation per cluster. found is false for an env the control plane has
// never seen (its first deploy creates it).
func readHubReadings(ctx context.Context, ledger envLedger, env string) (readings []hubReading, found bool, err error) {
	hosted, ok := ledger.Bindings.(*hostedStore)
	if !ok {
		return nil, false, fmt.Errorf("env %q is hub-converged but its ledger is %T, not a control plane", env, ledger.Bindings)
	}
	envID, err := hosted.envID(ctx, env)
	if err != nil {
		if errors.Is(err, errHostedEnvNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var resp struct {
		Convergences []hubReading `json:"convergences"`
	}
	if err := hosted.client.Call(ctx, procListConvergencesCLI, map[string]any{"environmentId": envID, "limit": 50}, &resp); err != nil {
		return nil, true, err
	}
	newest := map[[2]string]hubReading{}
	for _, r := range resp.Convergences { // newest first
		k := [2]string{r.Cluster, r.Tree}
		if _, seen := newest[k]; !seen {
			newest[k] = r
		}
	}
	for _, r := range newest {
		readings = append(readings, r)
	}
	sort.Slice(readings, func(i, j int) bool {
		if readings[i].Cluster != readings[j].Cluster {
			return readings[i].Cluster < readings[j].Cluster
		}
		return readings[i].Tree < readings[j].Tree
	})
	return readings, true, nil
}

// refuseUnreadyHub refuses to record a promotion for a hub-converged env whose
// reconciler is failing, with the hub's own words.
//
// A cluster whose newest reading is "failed" refuses — except ArtifactFailed,
// which is what a Kustomization says before it has ever had a bundle to pull,
// and which recording this promotion is exactly what fixes. An env that is
// already bound to a release but has NO reading at all refuses too: the hub has
// never reported reconciling it, which is what missing Kustomizations look
// like. A control plane that cannot be read is said and not refused: an
// unreadable status is not evidence the hub is broken.
func refuseUnreadyHub(ctx context.Context, env string, ledger envLedger, bound bool, o promoteFollowOptions) error {
	if !ledger.hubUnits() {
		return nil
	}
	readings, found, err := readHubReadings(ctx, ledger, env)
	if err != nil {
		o.notice("\n[deploy] Note: the hub's reconcile status for %s could not be read (%v); recording without it.\n", env, err)
		return nil
	}
	if !found {
		return nil
	}
	var failing []string
	for _, r := range readings {
		if r.State == "failed" && r.Reason != "ArtifactFailed" {
			where := emptyAs(r.Cluster, "(cluster not named)")
			if r.Tree != "" {
				where = r.Tree
			}
			failing = append(failing, fmt.Sprintf("    %s: %s", where,
				oneLine(strings.Join(nonEmptyStrings(r.Reason, r.Message), ": "), 400)))
		}
	}
	switch {
	case len(failing) > 0:
		return &exitCodeError{code: exitConflict, msg: fmt.Sprintf(
			"env %q is converged by its control plane's hub, and the hub's reconciler is FAILING:\n%s\n"+
				"NOTHING WAS RECORDED: a promotion recorded now would wait out its budget and end UNKNOWN, "+
				"because nothing can apply it.\n"+
				"  Fix what the hub reports (often the reconciler identity's rights on the connected cluster), "+
				"then re-run; `forge env status %s` shows the hub's view. --skip-hub-check records anyway.",
			env, strings.Join(failing, "\n"), env)}
	case bound && len(readings) == 0:
		return &exitCodeError{code: exitConflict, msg: fmt.Sprintf(
			"env %q is converged by its control plane's hub and is bound to a release, but the hub has never "+
				"reported reconciling it — its Kustomizations may not exist.\n"+
				"NOTHING WAS RECORDED. Check the env's connected clusters (`forge cluster list`) and the hub, "+
				"then re-run; --skip-hub-check records anyway.", env)}
	}
	return nil
}

func nonEmptyStrings(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// deployApplyPath names, in one line, which machinery ships an env —
// what `forge env deploy <env> --explain` prints so the operator does not
// have to infer it from the ledger's flags.
func deployApplyPath(ledger envLedger) string {
	switch {
	case ledger.hubUnits() && !ledger.Mixed:
		return "hub converge — forge records the promotion and bundle; the control plane's reconciler applies it to the connected clusters; nothing is applied from this machine"
	case ledger.hubUnits():
		return "split — the control plane's hub converges " + strings.Join(ledger.Units.Hub, ", ") +
			" from the recorded bundle; forge applies " + strings.Join(ledger.Units.Forge, ", ") + " (and anything outside a unit) FROM THIS MACHINE"
	case ledger.ledgerOnly():
		return "ledger only — forge applies the whole env FROM THIS MACHINE (kubectl, server-side apply); the control plane only records the promotion and converges nothing, because the env binds no hosted tier"
	case ledger.Hosted && ledger.Mixed:
		return "mixed — forge applies the cluster/compose/frontend half FROM THIS MACHINE (kubectl, server-side apply), and the control plane converges the hosted half"
	case ledger.Hosted:
		return "hosted — forge publishes to the control plane, which runs it; nothing is applied from this machine"
	default:
		return "client apply — forge applies from this machine (kubectl, server-side apply)"
	}
}
