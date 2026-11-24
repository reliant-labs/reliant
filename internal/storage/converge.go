package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Facts are what a forge command ALREADY knows about the local caches it just
// used: the project it ran in, the k3d contexts the env declares, the local
// registry container those contexts pull from, that registry's host aliases,
// the repositories this build pushes, and the project's release pins.
//
// Every field is optional. Converge records what it is given and leaves the
// rest of the policy alone — a cluster phase knows contexts and aliases but no
// repositories, a build knows all five. That is why activation does not need a
// command of its own: the facts arrive from whichever touch point happens to
// run, and accumulate.
type Facts struct {
	// Project is the project directory whose rotated logs forge may expire.
	// Stored absolute.
	Project string
	// Contexts are kubectl contexts whose workloads protect images in the
	// registry. Only local `k3d-` contexts are accepted: retention reads the
	// protected set from them, and a remote cluster forge cannot list would
	// make deletion unsafe.
	Contexts []string
	// Registry is the local registry's CONTAINER name (`k3d-…`), which is
	// also how `forge storage gc` addresses it.
	Registry string
	// Aliases are every host name under which that one registry is pushed to
	// or pulled from — the containerd mirror keys plus the in-network
	// `<container>:5000`. A repository is attributed to this registry only
	// when its host is one of these.
	Aliases []string
	// Repositories are full image references, registry host included. The
	// host is matched against Aliases and then stripped: retention works in
	// repository paths, as the registry API does.
	Repositories []string
	// Pins are image digests that must survive retention regardless of age.
	Pins []string
	// Repos are git repositories (any path inside one) whose worktrees the
	// worktree layer may reclaim: the project's own and its build siblings.
	Repos []string
}

// Converge upserts Facts into the machine storage policy, under the same lock
// every other policy writer takes.
//
// Three properties make this safe to call from a hot path, on every run:
//
//   - IDEMPOTENT. Converging the same facts twice leaves the file untouched
//     (it is not even rewritten), so warm runs cost one read.
//   - ADDITIVE ONLY. Nothing is ever removed here. Several projects on one
//     machine converge into the same policy, and a project that stops using a
//     context must not silently un-protect it for everybody else. Entries that
//     are GONE — a cluster no longer in kubeconfig or docker, a deleted
//     project directory — are removed by Prune at the start of GC, never here.
//     A project under the temp dir is never added at all.
//   - COMPLETE-OR-NOTHING for a registry. A registry becomes a cleanup target
//     only once the container, its aliases, the protecting contexts and at
//     least one repository are all known. Recording a registry whose
//     protecting contexts were still unknown would authorize deletion against
//     an unknown protected set, which is the one failure mode retention must
//     never have. Partial facts still converge their project/context/pin half.
func Converge(policyPath string, f Facts) error {
	return WithLock(policyPath, func() error {
		p, err := Load(policyPath)
		if err != nil {
			return err
		}
		next, changed := upsertFacts(p, f)
		if !changed {
			return nil
		}
		if err := Save(policyPath, next); err != nil {
			return fmt.Errorf("converge local storage ownership: %w", err)
		}
		return nil
	})
}

// upsertFacts is the pure half of Converge: policy + facts -> policy, plus
// whether anything actually changed. Register (which DISCOVERS the same facts
// from live docker metadata instead of being told them) applies its findings
// through this function too, so there is one merge rule rather than two.
func upsertFacts(p Policy, f Facts) (Policy, bool) {
	before := p.fingerprint()

	// A project under the temp dir is a test fixture or a scratch scaffold,
	// never one whose logs should be maintained (see underTempDir).
	if f.Project != "" {
		if absolute, err := filepath.Abs(f.Project); err == nil && !underTempDir(absolute) && !contains(p.Projects, absolute) {
			p.Projects = append(p.Projects, absolute)
		}
	}
	for _, c := range f.Contexts {
		if isLocalK3d(c) && !contains(p.Clusters, c) {
			p.Clusters = append(p.Clusters, c)
		}
	}
	for _, pin := range f.Pins {
		if pin != "" && !contains(p.Pins, pin) {
			p.Pins = append(p.Pins, pin)
		}
	}
	for _, repo := range f.Repos {
		if absolute, err := filepath.Abs(repo); err == nil && repo != "" && !underTempDir(absolute) && !contains(p.Repos, absolute) {
			p.Repos = append(p.Repos, absolute)
		}
	}
	p.Registries = upsertRegistry(p.Registries, p.Clusters, f)

	return p, p.fingerprint() != before
}

// upsertRegistry merges one registry's facts into the registered set.
//
// The contexts recorded are the union with EVERY registered local context, not
// just the ones these facts named. Retention protects an image when some
// registered context still references it, so a context missing from the list is
// an image deleted out from under a running cluster, while a context listed
// that never pulls from this registry only ever widens the protected set. The
// asymmetry is deliberate: over-protection wastes disk, under-protection breaks
// a cluster.
func upsertRegistry(registries []Registry, allContexts []string, f Facts) []Registry {
	if !isLocalK3d(f.Registry) {
		return registries
	}
	index := -1
	reg := Registry{Container: f.Registry}
	for i, old := range registries {
		if old.Container == f.Registry {
			index, reg = i, old
			break
		}
	}
	for _, alias := range f.Aliases {
		if alias != "" && !contains(reg.Aliases, alias) {
			reg.Aliases = append(reg.Aliases, alias)
		}
	}
	for _, c := range allContexts {
		if isLocalK3d(c) && !contains(reg.Contexts, c) {
			reg.Contexts = append(reg.Contexts, c)
		}
	}
	for _, repository := range f.Repositories {
		host, path, ok := splitRepository(repository)
		if ok && contains(reg.Aliases, host) && !contains(reg.Repositories, path) {
			reg.Repositories = append(reg.Repositories, path)
		}
	}
	// The completeness gate. An entry that cannot yet be acted on safely is
	// not written at all, so the policy never describes a half-known target.
	if len(reg.Aliases) == 0 || len(reg.Contexts) == 0 || len(reg.Repositories) == 0 {
		return registries
	}
	if index >= 0 {
		registries[index] = reg
		return registries
	}
	return append(registries, reg)
}

// splitRepository separates an image reference's registry host from its
// repository path. Only a first segment that looks like a host:port — which is
// what every local registry alias is — counts, so `library/app` is not read as
// host `library`.
func splitRepository(repository string) (host, path string, ok bool) {
	host, path, found := strings.Cut(repository, "/")
	if !found || host == "" || path == "" {
		return "", "", false
	}
	if strings.Index(host, ":") <= 0 {
		return "", "", false
	}
	return host, path, true
}

// isLocalK3d gates both contexts and container names on the `k3d-` prefix
// Policy.Validate also enforces, so Converge can never write an entry Save
// would reject.
func isLocalK3d(name string) bool { return strings.HasPrefix(name, "k3d-") && len(name) > len("k3d-") }

// fingerprint is a stable rendering of everything Converge can change, so
// "did this converge alter the policy" is one comparison rather than a
// field-by-field diff that a new field could silently fall out of.
func (p Policy) fingerprint() string {
	type registryPrint struct {
		Container                   string
		Repositories, Aliases, Ctxs []string
	}
	snapshot := struct {
		Projects, Clusters, Pins, Repos []string
		Registries                      []registryPrint
	}{Projects: sorted(p.Projects), Clusters: sorted(p.Clusters), Pins: sorted(p.Pins), Repos: sorted(p.Repos)}
	for _, r := range p.Registries {
		snapshot.Registries = append(snapshot.Registries, registryPrint{
			Container: r.Container, Repositories: sorted(r.Repositories),
			Aliases: sorted(r.Aliases), Ctxs: sorted(r.Contexts),
		})
	}
	sort.Slice(snapshot.Registries, func(i, j int) bool {
		return snapshot.Registries[i].Container < snapshot.Registries[j].Container
	})
	b, _ := json.Marshal(snapshot)
	return string(b)
}

func sorted(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}

// GoCacheGC runs only the Go-toolchain caches (GoCaches), with the whole of
// ctx's budget rather than the slice a full pass gives them.
//
// It exists because the shared build cache fills on a different clock from
// everything else: under agent load ~25 GiB an hour crosses the trim's 2h
// safety floor, so a trim that runs hourly — or one whose share of a larger
// pass runs out under load — leaves tens of GiB that could already go on the
// disk. This pass is cheap enough (one walk of the cache, paced unlinks, no
// Docker, no registry) to run every few minutes, and it is non-disruptive for
// the same reason the Go-cache layer of NonDisruptiveGC is.
func (r Runner) GoCacheGC(ctx context.Context, apply bool) error {
	if err := r.Policy.Validate(); err != nil {
		return err
	}
	r = r.beginPass(ctx)
	defer r.endPass()
	return r.GoCaches(apply)
}

// NonDisruptiveGC runs only the layers that cannot interrupt anything a
// developer is currently using: expiring rotated logs, trimming the Go caches,
// removing dangling images, evicting unused builder cache, the temp sweep and
// the source cache.
//
// Registry GC and node reconfiguration are deliberately NOT here. Registry
// cleanup takes the registry offline for the duration (pushes and pulls fail),
// and node reconfiguration restarts kubelet — both are fine for a scheduled
// 03:30 pass and unacceptable as a side effect of `forge env up`, which is
// typically the command that is about to push to that registry.
//
// Worktrees are not here either, not even as a preview. This pass runs behind
// the user's back — at the end of `forge env up`, and hourly from the
// installed schedule — and nothing it can observe tells an abandoned worktree
// from one an agent is between two commands in: no process holds it open, and
// its files can sit untouched for a day while the agent reads. Removing one is
// a decision only an explicit `forge storage gc --apply` may take. Anonymous
// volumes are the same decision about data: this pass records which ones are
// unused, so their clocks start, and never removes one (anonvolumes.go).
//
// As in GC, each layer's failure is recorded and the rest still run — unless
// the filesystem reported distress, which stops the pass (deletes.go).
func (r Runner) NonDisruptiveGC(ctx context.Context, apply bool) error {
	if err := r.Policy.Validate(); err != nil {
		return err
	}
	r = r.beginPass(ctx)
	defer r.endPass()
	var failures []error
	add := func(err error) {
		if err != nil {
			failures = append(failures, err)
		}
	}
	add(r.runLayer("logs", shareLogs, func(s Runner) error { return s.Logs(apply) }))
	// Go caches FIRST after the cheap log expiry: the shared build cache grows
	// ~40 GB/h under agent load and is the layer that decides whether the disk
	// fills. Every layer below runs in its own slice of the pass budget (see
	// passctx.go), so none starves another, and a layer cut off by its slice
	// is recorded as cut off, not failed.
	add(r.goCacheLayer(apply))
	// A nonlocal Docker endpoint ends only the Docker layer; the layers below
	// never touch Docker.
	dockerErr := r.runLayer("docker", shareDocker, func(s Runner) error { return s.Local(ctx) })
	add(dockerErr)
	if dockerErr == nil {
		add(r.runLayer("local-registry images", shareDocker, func(s Runner) error { return s.LocalImages(s.hostCtx(), apply) }))
		add(r.runLayer("dangling images", shareDocker, func(s Runner) error { return s.DanglingImages(s.hostCtx(), apply) }))
		// Observe only: this pass starts each anonymous volume's unused
		// clock but never removes one, whatever the runner was told
		// (anonvolumes.go).
		add(r.runLayer("anonymous volumes", shareDocker, func(s Runner) error {
			s.Explicit = false
			return s.AnonymousVolumes(s.hostCtx(), apply)
		}))
		for _, builder := range r.Policy.Builders {
			add(r.runLayer("builder "+builder, shareDocker, func(s Runner) error { return s.builderGC(s.hostCtx(), builder, apply) }))
		}
	}
	// The temp sweep touches only $TMPDIR scratch under a fixed allowlist of
	// known-dead prefixes, so it is safe alongside a running stack.
	add(r.runLayer("temp sweep", shareTemp, func(s Runner) error { return s.TempSweep(apply) }))
	// Source eviction cannot interrupt a running stack. Resolve touches an
	// entry's metadata on every cache HIT, so anything a build is using now is
	// minutes old and cannot be in a set whose youngest member is
	// SourceCacheUnused (14 days) stale; the per-repository keep floor retains
	// an idle project's current pin; and an entry a live process holds open is
	// detected and retained. The worst case for being wrong is one re-clone of
	// a re-fetchable pin, the same cost shape as a pruned build cache.
	add(r.runLayer("source cache", shareSources, func(s Runner) error { return s.Sources(apply) }))
	return errors.Join(failures...)
}
