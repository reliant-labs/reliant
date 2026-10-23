package cli

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/deploystate"
)

// reconcilePolicyStore is the one capability this file needs: read an
// environment's reconcile policy. Declared HERE, at the consumer, rather
// than importing deploystate.Store wholesale — internal/cli has no
// business knowing how to write a record or list an environment, and a
// one-method interface is what makes the test double below three lines
// instead of a mock of five methods it never calls.
type reconcilePolicyStore interface {
	Policy(ctx context.Context, env string) (deploystate.Policy, error)
}

// errEnvironmentPinned is what a caller sees when an environment refuses
// changes. Phrased as an instruction rather than a status because the
// person reading it is mid-deploy and needs to know how to proceed.
type errEnvironmentPinned struct {
	env  string
	path string
}

func (e errEnvironmentPinned) Error() string {
	return fmt.Sprintf(
		"environment %q is pinned: forge will not change it.\n"+
			"  Something — a freeze, an incident, a hand-driven migration — set this deliberately.\n"+
			"  Drift is still being reported; only WRITES are refused.\n"+
			"  To lift it: set \"policy\" to \"observe\" or \"converge\" in %s",
		e.env, e.path)
}

// gateDeployOnPolicy refuses a deploy when the environment is pinned.
//
// # Why the read happens here, on every invocation
//
// The policy is read at the START of each deploy, from the store, and is
// never cached across invocations or baked into a rendered artifact.
// That is the whole design: an engineer who pins an environment
// mid-incident has stopped the NEXT deploy, not the one after the next
// release. A policy captured into an artifact at build time, or read
// once into a long-lived struct, would pass every test here and be wrong
// at the only moment anyone cares.
//
// # Why a read failure does NOT block the deploy
//
// Deliberate, and the opposite of the choice made in
// deploystate.DecideEnv. The two callers face different questions.
// DecideEnv is deciding whether to write AUTONOMOUSLY, so an unreadable
// policy must abort — acting without knowing whether you were permitted
// to is the failure that makes people distrust reconcilers. This gate
// sits in front of a HUMAN who typed `forge env deploy`, and a corrupt
// state file is not a reason to refuse them their own deploy. It warns
// loudly and proceeds; the human is the authority here, and they are
// present.
func gateDeployOnPolicy(ctx context.Context, store reconcilePolicyStore, env, policyPath string) error {
	if store == nil || env == "" {
		return nil
	}
	policy, err := store.Policy(ctx, env)
	if err != nil {
		fmt.Printf("  Warning: could not read the reconcile policy for %q (%v).\n"+
			"           Proceeding — a human asked for this deploy.\n", env, err)
		return nil
	}
	if !policy.AllowsChange() {
		return errEnvironmentPinned{env: env, path: policyPath}
	}
	return nil
}
