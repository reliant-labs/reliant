package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// Converge is additive only, so without pruning the policy only ever grows,
// and two kinds of entries go stale:
//
//   - CLUSTERS that no longer exist. A cluster deleted and recreated under a
//     new name (k3d-control-plane -> k3d-control-plane-v2) stays registered,
//     upsertRegistry copies it into every registry's protecting contexts, and
//     the protected-set scan of a context that does not exist fails. Registry
//     GC then refuses on every pass, forever: fail-closed, but permanently
//     off.
//   - PROJECTS whose directory is gone: deleted checkouts, and the temp
//     projects of tests.
//
// The rule for a cluster is deliberately narrow. It is dropped only when BOTH
// sources agree it is gone: its context is absent from kubeconfig, AND no
// container, running or stopped, carries its k3d.cluster label. A context that
// exists but is unreachable, or a cluster whose nodes are merely stopped,
// stays registered, and registry deletion keeps failing closed on it. If
// either source cannot be read, nothing is dropped.
//
// Dropping a cluster that turns out to be alive cannot cause a deletion.
// verifyConsumers refuses registry GC if any k3d server container sharing the
// registry's network belongs to a context the registry does not list. A wrong
// prune therefore turns into a refusal, not a deleted image.

// Prune removes stale clusters and projects from the policy at policyPath and
// saves it. The caller must hold the maintenance lock (WithLock), as GC does.
func (r Runner) Prune(ctx context.Context, policyPath string) error {
	p, err := Load(policyPath)
	if err != nil {
		return err
	}
	r.Policy = p
	next, changed := r.pruned(ctx)
	if !changed {
		return nil
	}
	return Save(policyPath, next)
}

// pruned is the policy with stale entries removed, and whether anything was.
// It writes nothing; GC uses the result for a preview without saving it, so
// the preview shows what an apply would do.
func (r Runner) pruned(ctx context.Context) (Policy, bool) {
	p := r.Policy
	before := p.fingerprint()

	var projects []string
	for _, project := range p.Projects {
		if _, err := os.Stat(project); os.IsNotExist(err) {
			r.print("storage policy: dropping project %s (directory no longer exists)\n", project)
			continue
		}
		projects = append(projects, project)
	}
	p.Projects = projects

	if gone := r.goneClusters(ctx, p.Clusters); len(gone) > 0 {
		p.Clusters = without(p.Clusters, gone)
		var registries []Registry
		for _, reg := range p.Registries {
			reg.Contexts = without(reg.Contexts, gone)
			if len(reg.Contexts) == 0 {
				// A registry with no protecting context fails Validate, which
				// would make the whole policy unloadable. Every consumer it
				// was registered for is gone, so it stops being a cleanup
				// target. The next converge from a project that uses it
				// re-adds it, with that project's contexts.
				r.print("storage policy: dropping registry %s (every cluster that protected it is gone)\n", reg.Container)
				continue
			}
			registries = append(registries, reg)
		}
		p.Registries = registries
	}
	p.Registries = r.withoutAbsentRegistries(ctx, p.Registries)
	return p, p.fingerprint() != before
}

// withoutAbsentRegistries drops registries whose container does not exist, as
// a clean docker listing (not an inspect error) reports. An e2e cluster's
// registry dies with the cluster, but nothing removed it from the policy, and
// every later pass then failed on `docker inspect` for each one. A listing that
// errors keeps everything. Re-registration on the next converge brings back a
// registry that comes back.
func (r Runner) withoutAbsentRegistries(ctx context.Context, registries []Registry) []Registry {
	if len(registries) == 0 {
		return registries
	}
	if err := r.Local(ctx); err != nil {
		return registries
	}
	var kept []Registry
	for _, reg := range registries {
		b, err := r.docker(ctx, "ps", "-aq", "--filter", "name=^/"+reg.Container+"$")
		if err == nil && strings.TrimSpace(string(b)) == "" {
			r.print("storage policy: dropping registry %s (its container no longer exists)\n", reg.Container)
			continue
		}
		kept = append(kept, reg)
	}
	return kept
}

// goneClusters returns the registered clusters that both kubeconfig and docker
// agree no longer exist. Any source it cannot read answers "keep everything".
func (r Runner) goneClusters(ctx context.Context, clusters []string) map[string]bool {
	if len(clusters) == 0 {
		return nil
	}
	b, err := r.command(ctx, "kubectl", "config", "get-contexts", "-o", "name")
	if err != nil {
		r.print("storage policy: not pruning clusters (cannot read kubeconfig contexts: %v)\n", err)
		return nil
	}
	known := map[string]bool{}
	for _, name := range strings.Fields(string(b)) {
		known[name] = true
	}
	// An empty answer most likely means the wrong kubeconfig, not "every
	// cluster is gone".
	if len(known) == 0 {
		return nil
	}
	// Docker's answer only counts if it is about THIS machine.
	if err := r.Local(ctx); err != nil {
		r.print("storage policy: not pruning clusters (%v)\n", err)
		return nil
	}
	gone := map[string]bool{}
	for _, cluster := range clusters {
		if known[cluster] || !isLocalK3d(cluster) {
			continue
		}
		b, err := r.docker(ctx, "ps", "-aq", "--filter", "label=k3d.cluster="+strings.TrimPrefix(cluster, "k3d-"))
		if err != nil {
			r.print("storage policy: keeping cluster %s (cannot list its containers: %v)\n", cluster, err)
			continue
		}
		if strings.TrimSpace(string(b)) != "" {
			continue // context deleted, but its nodes still exist (maybe stopped)
		}
		r.print("storage policy: dropping cluster %s (no kubeconfig context and no k3d containers)\n", cluster)
		gone[cluster] = true
	}
	return gone
}

func without(xs []string, drop map[string]bool) []string {
	var out []string
	for _, x := range xs {
		if !drop[x] {
			out = append(out, x)
		}
	}
	return out
}

// underTempDir reports whether path is inside the system temp directory,
// comparing resolved paths so /var/folders and /private/var/folders (macOS) or
// a symlinked /tmp agree. A project there is a test fixture or a scratch
// scaffold, which must never become a registered project: Logs expires files
// under every registered project, and one machine's policy had accumulated 65
// leaked TestBuildTag_*/001 projects this way.
func underTempDir(path string) bool {
	resolve := func(p string) string {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return real
		}
		return filepath.Clean(p)
	}
	tmp := resolve(os.TempDir())
	target := resolve(path)
	return target == tmp || strings.HasPrefix(target, tmp+string(filepath.Separator))
}
