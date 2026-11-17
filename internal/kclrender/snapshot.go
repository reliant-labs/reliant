package kclrender

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/reliant-labs/forge/pkg/release"
)

type snapshotKey struct{}

// snapshotPinsKey holds the same pins as a map, for the Go renderer of an env
// document (internal/envdoc), which binds them without a KCL evaluation.
type snapshotPinsKey struct{}

// SnapshotPins are the per-workload, per-slot references WithSnapshot bound to
// ctx (workload → slot → reference), and whether the render is immutable
// (every slot must be bound). ok is false when no snapshot is bound.
func SnapshotPins(ctx context.Context) (pins map[string]map[string]string, ok bool) {
	pins, ok = ctx.Value(snapshotPinsKey{}).(map[string]map[string]string)
	return pins, ok
}

// WithSnapshot validates and freezes the complete render pin context. Duplicate
// workload names across destinations with differing pins refuse because today's
// KCL binding is workload-relative; render those destinations separately instead.
func WithSnapshot(ctx context.Context, s release.Snapshot) (context.Context, error) {
	if err := s.Validate(); err != nil {
		return ctx, err
	}
	pins := map[string]map[string]string{}
	for _, b := range s.Bindings {
		a := s.Artifacts[b.Artifact]
		if a.Kind != release.KindOCI {
			continue
		}
		if pins[b.Consumer.Workload] == nil {
			pins[b.Consumer.Workload] = map[string]string{}
		}
		ref := release.BindingReference(a, b)
		if old, ok := pins[b.Consumer.Workload][b.Slot]; ok && old != ref {
			return ctx, fmt.Errorf("%w: conflicting cross-cluster workload slot", release.ErrInvalid)
		}
		pins[b.Consumer.Workload][b.Slot] = ref
	}
	data, err := json.Marshal(pins)
	if err != nil {
		return ctx, err
	}
	ctx = context.WithValue(ctx, snapshotPinsKey{}, pins)
	return context.WithValue(ctx, snapshotKey{}, []string{"forge_workload_image_digests=" + strconv.Quote(string(data)), "forge_immutable_release=true"}), nil
}

// SnapshotDArgs binds exact per-workload/named-slot references. KCL's immutable
// switch makes a missing slot an error rather than a repository/tag fallback.
// Callers render each cluster separately if duplicate workload names exist.
func SnapshotDArgs(s release.Snapshot, cluster string) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	pins := map[string]map[string]string{}
	for _, b := range s.Bindings {
		if b.Consumer.Cluster != cluster {
			continue
		}
		a := s.Artifacts[b.Artifact]
		if a.Kind != release.KindOCI {
			continue
		}
		if pins[b.Consumer.Workload] == nil {
			pins[b.Consumer.Workload] = map[string]string{}
		}
		pins[b.Consumer.Workload][b.Slot] = release.BindingReference(a, b)
	}
	if len(pins) == 0 {
		return nil, fmt.Errorf("%w: no OCI bindings for cluster %q", release.ErrInvalid, cluster)
	}
	data, err := json.Marshal(pins)
	if err != nil {
		return nil, err
	}
	return []string{"forge_workload_image_digests=" + strconv.Quote(string(data)), "forge_immutable_release=true"}, nil
}
