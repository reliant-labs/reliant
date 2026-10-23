package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// hostedCapacityPreflight asks the env's control plane whether its hosted part
// fits the org's compute plan, BEFORE anything is built, pushed or recorded.
// It writes nothing. builds is how many images this command would build.
//
//   - refused or undecidable → *deploytarget.CapacityRefusedError;
//   - a control plane that does not serve the check → a one-line warning and
//     nil (the server still enforces at RecordBundle / Promote);
//   - any other failure (network, auth) → that error, as the other hosted RPCs.
//
// An env with nothing hosted is not checked.
func hostedCapacityPreflight(ctx context.Context, envName string, entities *KCLEntities, builds int, out io.Writer) (*promotePlanCapacity, error) {
	if entities == nil || !entities.HasHosted() || entities.ControlPlane == nil {
		return nil, nil
	}
	group, err := buildHostedGroup(envName, entities)
	if err != nil || group == nil {
		// The deploy's own plan reports this refusal with its fix.
		return nil, nil
	}
	items, err := deploytarget.PreflightHosted(*group)
	if err != nil {
		return nil, nil
	}
	demand := deploytarget.DemandOf(items, builds)

	ep, err := cloud.ResolveEndpoint(envName, declarationFromEntities(entities))
	if err != nil {
		return nil, err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return nil, fmt.Errorf("env %q deploys to the control plane at %s: %w", envName, ep.URL, err)
	}
	client := hostedDeployClient(ep, cred)
	ref := hostedEnvRefFor(envName, entities)
	envID := ""
	if id, lerr := deploytarget.LookupHostedEnvironment(ctx, client, ref.Project, envName); lerr == nil {
		envID = id
	} else if !errors.Is(lerr, deploytarget.ErrHostedEnvironmentNotFound) {
		return nil, lerr
	}
	verdict, err := deploytarget.CheckHostedCapacity(ctx, client, envID, demand)
	if err != nil {
		return nil, err
	}
	report := &promotePlanCapacity{Verdict: verdict, Demand: demand}
	switch {
	case verdict.Unchecked:
		fmt.Fprintf(out, "WARNING: capacity could not be pre-checked: %s\n", verdict.Summary())
	case verdict.Queued():
		// NOT a refusal. The control plane will accept this deploy and
		// queue it on a person (billing), so the build, the record and the
		// promote all still run — that is what lets it go live with no
		// re-run once they act. Said up front, before a nine-minute build,
		// so nobody is surprised by the queue at the end of it.
		fmt.Fprintf(out, "Capacity: this deploy will be QUEUED on %s — %s.\n"+
			"  It is still built, recorded and promoted now, and goes live by itself once that is done.\n",
			verdict.Holds[0].Label(), verdict.Reason)
	case !verdict.Allowed:
		return report, &deploytarget.CapacityRefusedError{Env: envName, Verdict: verdict, Demand: demand}
	}
	return report, nil
}

// buildCapacityPreflight is hostedCapacityPreflight for `forge env build`.
//
// A BUILD NEVER NEEDS A REACHABLE CONTROL PLANE (the rule recordEnvBuildDeclaration
// states): the pre-flight only saves a no-plan org a wasted build. When it cannot
// be DELIVERED — no credential, a transport error, Unavailable — the build warns
// and carries on. An answered refusal (CapacityRefusedError) is a fact about the
// org and still stops it. The deploy path keeps the strict form.
func buildCapacityPreflight(ctx context.Context, envName string, entities *KCLEntities, builds int, out io.Writer) (*promotePlanCapacity, error) {
	capacity, err := hostedCapacityPreflight(ctx, envName, entities, builds, out)
	if err == nil {
		return capacity, nil
	}
	var refused *deploytarget.CapacityRefusedError
	if errors.As(err, &refused) || !declarationUndeliverable(err) {
		return capacity, err
	}
	fmt.Fprintf(out, "[capacity] Warning: capacity pre-flight skipped for env %s: %v\n"+
		"[capacity]   The build continues; the deploy re-checks capacity when it reaches the control plane.\n", envName, err)
	return nil, nil
}

// hostedBuildCount is how many images a deploy of this env would build: the
// hosted workloads that declare an image this project builds.
func hostedBuildCount(e *KCLEntities) int {
	n := 0
	for _, w := range e.Workloads {
		if w.OnRuntime(RuntimeHosted) && w.Image != "" {
			n++
		}
	}
	return n
}

// hostedCapacityPreflightForEnv renders envName and runs the pre-flight over
// its hosted part. A render failure is not this check's to report: the
// command's own render fails with the real diagnosis.
func hostedCapacityPreflightForEnv(ctx context.Context, projectDir, envName string, withBuilds bool, out io.Writer) (*promotePlanCapacity, error) {
	entities, err := RenderKCL(ctx, projectDir, envName)
	if err != nil || entities == nil {
		return nil, nil
	}
	builds := 0
	if withBuilds {
		builds = hostedBuildCount(entities)
	}
	return hostedCapacityPreflight(ctx, envName, entities, builds, out)
}

func entitiesOrEmpty(e *KCLEntities) *KCLEntities {
	if e == nil {
		return &KCLEntities{}
	}
	return e
}

// promotePlanCapacity is the pre-flight's answer as the deploy plan carries it,
// so --plan-only and the post-apply summary show the same Capacity section.
type promotePlanCapacity struct {
	Verdict deploytarget.CapacityVerdict `json:"verdict"`
	Demand  deploytarget.CapacityDemand  `json:"demand"`
}

func renderCapacitySection(out io.Writer, c *promotePlanCapacity) {
	if c == nil {
		return
	}
	fmt.Fprintf(out, "\nCapacity  %s\n", c.Verdict.Summary())
	fmt.Fprintf(out, "  demand    %s\n", c.Demand)
	if c.Verdict.HasComputePlan {
		fmt.Fprintf(out, "  ceiling   %dm CPU, %s memory, %d GiB storage\n", c.Verdict.CeilingCPU, gibStringCLI(c.Verdict.CeilingMemory), c.Verdict.CeilingStorage)
	}
	switch {
	case c.Verdict.Queued():
		// The HOLD's remedy, not the refusal's: a queued deploy needs no
		// retry, and the refusal's "then retry" would say otherwise.
		if fix := c.Verdict.Holds[0].Fix; fix != "" {
			fmt.Fprintf(out, "  do        %s\n", fix)
		}
	case !c.Verdict.Allowed && !c.Verdict.Unchecked && c.Verdict.Fix != "":
		fmt.Fprintf(out, "  fix       %s\n", c.Verdict.Fix)
	}
}

func gibStringCLI(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }
