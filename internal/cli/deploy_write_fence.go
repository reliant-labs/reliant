package cli

import (
	"context"

	"github.com/reliant-labs/forge/internal/cluster"
)

// fenceDeployPreview marks ctx so that no kubectl write can run under it when
// the invocation is a preview: --plan, --plan-only, --dry-run or --explain.
//
// Each of those promises the cluster is left exactly as it was. --plan-only is
// the stage a reviewer reads before approving prod, and a report of it
// applying the hub cluster group (88855ea7: "NOTHING WAS RECORDED" printed
// while every Deployment rolled) is the failure this exists to make
// impossible: whatever stage forgets to honour the dry-run flag, its kubectl
// write is refused by cluster.KubectlCommand before a process starts, with the
// verb named, instead of reaching the API server.
//
// It fences cluster writes only. --plan-only still builds, pushes and cuts the
// release as its flag says — those are registry and ledger writes the
// two-stage pipeline depends on, and the release they produce is inert until
// a promotion is recorded.
func fenceDeployPreview(ctx context.Context, f deployCmdFlags) context.Context {
	switch {
	case f.promote.planOnly:
		return cluster.WithWritesFenced(ctx, "--plan-only writes nothing to any cluster")
	case f.promote.plan:
		return cluster.WithWritesFenced(ctx, "--plan writes nothing to any cluster")
	case f.dryRun:
		return cluster.WithWritesFenced(ctx, "--dry-run writes nothing to any cluster")
	case f.explain:
		return cluster.WithWritesFenced(ctx, "--explain writes nothing to any cluster")
	}
	return ctx
}

// fencePreflight marks the context of the pre-record preflight. That pass is a
// dry run on every deploy, the approving one included: it runs before the
// promotion is written, so anything it applied would ship a release the ledger
// does not name yet.
func fencePreflight(ctx context.Context) context.Context {
	return cluster.WithWritesFenced(ctx, "the pre-record preflight writes nothing to any cluster")
}
