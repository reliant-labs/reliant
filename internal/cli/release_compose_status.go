package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// verifySnapshotBindings never indexes by repository: API and worker can
// legitimately run different content from exactly the same repository.
func verifySnapshotBindings(s release.Snapshot, key string, running []cluster.WorkloadImage, readErr error) []imageVerification {
	var out []imageVerification
	for _, b := range s.Bindings {
		if b.Consumer.Cluster != key {
			continue
		}
		a := s.Artifacts[b.Artifact]
		result := imageVerification{Image: b.Consumer.Workload + "/" + b.Slot, Declared: a.Content, State: imageUnreachable, Detail: "non-runtime slot observation is unknown"}
		if readErr != nil {
			result.Detail = readErr.Error()
			out = append(out, result)
			continue
		}
		if !strings.HasPrefix(b.Slot, "container:") && !strings.HasPrefix(b.Slot, "init:") {
			out = append(out, result)
			continue
		}
		result.State = imageMissing
		result.Detail = "named consumer/container missing"
		name := strings.TrimPrefix(strings.TrimPrefix(b.Slot, "container:"), "init:")
		init := strings.HasPrefix(b.Slot, "init:")
		matches := 0
		for _, w := range running {
			if w.Name != b.Consumer.Workload || w.Container != name || w.Init != init || w.EnvVar != "" {
				continue
			}
			matches++
			p := parseImageRef(w.Image)
			result.Running = w.Image
			if p.Digest == "" {
				result.State = imageUntagged
				result.Detail = "runtime image is mutable"
			} else if p.Digest != a.Content || p.Repository != a.Location {
				result.State = imageDrift
				result.Detail = "named runtime slot differs from snapshot"
			} else {
				result.State = imageMatch
				result.Detail = "named runtime slot matches immutable binding"
			}
		}
		if matches > 1 {
			result.State = imageUnreachable
			result.Detail = "ambiguous runtime consumer slot observation"
		}
		out = append(out, result)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Image < out[j].Image })
	return out
}
func snapshotStatusImages(ctx context.Context, s release.Snapshot, opts envStatusOptions) []imageVerification {
	namespaces := map[string]string{}
	for _, r := range s.Resources {
		if r.Key.Namespace != "" {
			if old, ok := namespaces[r.Key.Cluster]; ok && old != r.Key.Namespace {
				namespaces[r.Key.Cluster] = "*"
			} else {
				namespaces[r.Key.Cluster] = r.Key.Namespace
			}
		}
	}
	var out []imageVerification
	seen := map[string]bool{}
	for _, c := range s.Consumers {
		if seen[c.Key.Cluster] {
			continue
		}
		seen[c.Key.Cluster] = true
		ns := namespaces[c.Key.Cluster]
		if ns == "" || ns == "*" || c.Key.Cluster == release.BundleHostedCluster {
			out = append(out, verifySnapshotBindings(s, c.Key.Cluster, nil, fmt.Errorf("snapshot observation requires an explicit per-consumer witness for destination %s", c.Key.Cluster))...)
			continue
		}
		read, cancel := context.WithTimeout(ctx, opts.Timeout)
		running, err := opts.Lister.ListWorkloadImages(read, c.Key.Cluster, ns)
		cancel()
		out = append(out, verifySnapshotBindings(s, c.Key.Cluster, running, err)...)
	}
	return out
}
