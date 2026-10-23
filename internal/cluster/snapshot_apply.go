package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// ApplySnapshot applies only recorded complete snapshot bytes through the normal
// config/CRD/pre-rollout/rollout actuator. It never evaluates KCL, renders Helm,
// provisions secrets, or prunes based on today's checkout. Cluster selection is
// explicit and must name a recorded destination. A failure does not claim that
// the complete snapshot was applied.
func ApplySnapshot(ctx context.Context, s release.Snapshot, clusterName string, opts ApplyOpts) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if opts.Context != clusterName || opts.Env != s.Environment {
		return fmt.Errorf("snapshot apply destination/scope mismatch")
	}
	if len(opts.Targets) > 0 || len(opts.HelmCharts) > 0 || opts.Prune || opts.PruneCRDs {
		return fmt.Errorf("snapshot apply forbids target filtering, charts and inferred pruning")
	}
	var subset release.Snapshot
	subset.Resources = nil
	for _, r := range s.Resources {
		if r.Key.Cluster == clusterName {
			subset.Resources = append(subset.Resources, r)
		}
	}
	if len(subset.Resources) == 0 {
		return fmt.Errorf("snapshot has no recorded objects for cluster %q", clusterName)
	}
	stream := bundle.SnapshotStream(subset)
	if strings.TrimSpace(stream) == "" {
		return fmt.Errorf("snapshot apply stream is empty")
	}
	// Existing CRD stamping is disabled: ownership is already in the sealed bytes.
	opts.Project = ""
	opts.ClusterScope = nil
	return applyRendered(ctx, opts, stream)
}
