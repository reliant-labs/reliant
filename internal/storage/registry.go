package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Tag is a registry alias and its last push timestamp.
type Tag struct {
	Repository string
	Name       string
	Digest     string
	Pushed     time.Time
}

// Version identifies one content-addressed registry manifest.
type Version struct {
	Repository string
	Digest     string
}

// Revision is one manifest link on the registry filesystem and the moment it was
// written. Untagged manifests have no push timestamp anywhere else, so the link
// mtime is the only available age for them.
type Revision struct {
	Version
	Modified time.Time
}

// PlanEntry is one manifest selected for deletion, with the reason it became
// eligible and a size estimate when the manifest could be read.
type PlanEntry struct {
	Version
	// Untagged marks a manifest no tag points at: an orphan left behind by a
	// re-push of a reused tag, unreachable from anything retained.
	Untagged bool
	// Tags are the aliases that disappear with this manifest.
	Tags  []string
	Bytes uint64
	Sized bool
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var releaseTagPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// RegistryCandidates protects aliases at digest granularity: deleting one
// manifest also removes every tag that points to it.
func RegistryCandidates(tags []Tag, p Policy, reg Registry, protected map[string]map[string]bool, now time.Time) []Version {
	var result []Version
	for _, repo := range reg.Repositories {
		var rows []Tag
		for _, t := range tags {
			if t.Repository == repo {
				rows = append(rows, t)
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Pushed.Equal(rows[j].Pushed) {
				return rows[i].Digest < rows[j].Digest
			}
			return rows[i].Pushed.After(rows[j].Pushed)
		})
		keep := map[string]bool{}
		ranked := map[string]bool{}
		for _, t := range rows {
			if !ranked[t.Digest] {
				ranked[t.Digest] = true
				if len(ranked) <= p.RegistryKeep {
					keep[t.Digest] = true
				}
			}
			if now.Sub(t.Pushed) <= time.Duration(p.RegistryDays)*24*time.Hour || protected["*"][t.Digest] || protected[repo][t.Name] || protected[repo][t.Digest] || releaseTagPattern.MatchString(t.Name) || t.Name == "dev" || t.Name == "e2e" || t.Name == "latest" || t.Name == "stable" || t.Name == "main" {
				keep[t.Digest] = true
			}
		}
		for digest := range ranked {
			if !keep[digest] {
				result = append(result, Version{repo, digest})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Repository == result[j].Repository {
			return result[i].Digest < result[j].Digest
		}
		return result[i].Repository < result[j].Repository
	})
	return result
}

// clusterReferences scans every listable resource in a cluster, not a fixed list
// of workload kinds. A custom resource can hold an image reference anywhere in
// its spec — workspaces.reliant.dev carries one at spec.template.image — and a
// kind allowlist silently leaves those unprotected.
func (r Runner) clusterReferences(ctx context.Context, cluster string) ([]string, error) {
	b, err := r.command(ctx, "kubectl", "--context", cluster, "--request-timeout=30s", "api-resources", "--verbs=list", "-o", "name")
	if err != nil {
		return nil, err
	}
	kinds := listableResources(b)
	if len(kinds) == 0 {
		return nil, fmt.Errorf("no listable resources reported")
	}
	var refs []string
	for _, batch := range resourceBatches(kinds, 25) {
		selector := strings.Join(batch, ",")
		b, err := r.command(ctx, "kubectl", "--context", cluster, "--request-timeout=120s", "get", selector, "-A", "--ignore-not-found", "-o", "json")
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", selector, err)
		}
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("listing %s: %w", selector, err)
		}
		refs = append(refs, referenceCandidates(v)...)
	}
	return refs, nil
}

func (r Runner) protected(ctx context.Context, reg Registry) (map[string]map[string]bool, error) {
	refs := append([]string{}, r.Policy.Pins...)
	for _, cluster := range reg.Contexts {
		clusterRefs, err := r.clusterReferences(ctx, cluster)
		if err != nil {
			return nil, fmt.Errorf("refusing registry cleanup: protected set for %s unavailable: %w", cluster, err)
		}
		refs = append(refs, clusterRefs...)
	}
	containerRefs, err := r.containerReferences(ctx)
	if err != nil {
		return nil, err
	}
	refs = append(refs, containerRefs...)
	return protectedRefs(refs, reg.Aliases), nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (r Runner) inventory(ctx context.Context, container string) ([]Tag, error) {
	script := `find /var/lib/registry/docker/registry/v2/repositories -path '*/_manifests/tags/*/current/link' -type f -exec sh -ec 'for p do printf "%s\t%s\t%s\n" "$(stat -c %Y "$p")" "$p" "$(cat "$p")"; done' sh {} +`
	b, err := r.docker(ctx, "exec", container, "sh", "-ec", script)
	if err != nil {
		return nil, err
	}
	var tags []Tag
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 3 || !digestPattern.MatchString(cols[2]) {
			return nil, fmt.Errorf("invalid registry tag inventory")
		}
		stamp, err := strconv.ParseInt(cols[0], 10, 64)
		if err != nil {
			return nil, err
		}
		path := strings.TrimPrefix(cols[1], "/var/lib/registry/docker/registry/v2/repositories/")
		repo, tag, ok := strings.Cut(path, "/_manifests/tags/")
		if !ok {
			return nil, fmt.Errorf("invalid registry path")
		}
		tags = append(tags, Tag{repo, strings.TrimSuffix(tag, "/current/link"), cols[2], time.Unix(stamp, 0)})
	}
	return tags, nil
}

// revisions inventories every manifest link, including untagged indexes. Their
// children must remain pullable even when a child has its own expired tag. The
// link mtime is carried through because an untagged manifest has no push
// timestamp anywhere else, and age is what makes one reclaimable.
func (r Runner) revisions(ctx context.Context, container string) ([]Revision, error) {
	script := `find /var/lib/registry/docker/registry/v2/repositories -path '*/_manifests/revisions/sha256/*/link' -type f -exec sh -ec 'for p do printf "%s\t%s\t%s\n" "$(stat -c %Y "$p")" "$p" "$(cat "$p")"; done' sh {} +`
	b, err := r.docker(ctx, "exec", container, "sh", "-ec", script)
	if err != nil {
		return nil, err
	}
	var revisions []Revision
	const prefix = "/var/lib/registry/docker/registry/v2/repositories/"
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 3 || !strings.HasPrefix(cols[1], prefix) || !digestPattern.MatchString(cols[2]) {
			return nil, fmt.Errorf("invalid registry revision inventory")
		}
		stamp, err := strconv.ParseInt(cols[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid registry revision timestamp: %w", err)
		}
		repo, revision, ok := strings.Cut(strings.TrimPrefix(cols[1], prefix), "/_manifests/revisions/sha256/")
		if !ok || repo == "" || revision != strings.TrimPrefix(cols[2], "sha256:")+"/link" {
			return nil, fmt.Errorf("invalid registry revision path")
		}
		revisions = append(revisions, Revision{Version{repo, cols[2]}, time.Unix(stamp, 0)})
	}
	return revisions, nil
}

// containerInfo is the subset of `docker inspect` output this package reads.
// The json tags name the real wire keys explicitly rather than leaning on
// encoding/json's case-insensitive fallback: the fallback makes the DECODER the
// only writer a reader can infer, which is invisible to static analysis (see
// internal/deadcodeguard), and it would silently keep matching if Docker ever
// changed a key's case.
type containerInfo struct {
	Image string `json:"Image"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Cmd []string `json:"Cmd"`
		Env []string `json:"Env"`
	} `json:"Config"`
	Mounts          []containerMount `json:"Mounts"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]any `json:"Networks"`
	} `json:"NetworkSettings"`
}

// containerMount is one entry of `docker inspect`'s Mounts. Name is set for a
// volume, Source for a bind.
type containerMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

// maintenanceMounts reproduces a container's mounts as `--mount` flags whose
// sources are all EXPLICIT: a volume by its name, a bind by its host path.
//
// This replaces `--volumes-from`, and the difference is the registry's data.
// k3d's registry keeps /var/lib/registry in an ANONYMOUS volume, and a
// container that inherits an anonymous volume through --volumes-from and is
// run with --rm removes that volume when it exits — moby's removeMountPoints
// spares only mounts with an explicit source, and ignores "in use". The
// registry container's own reference was all that kept every pushed image
// alive; if it was removed during a maintenance pass (a k3d registry delete
// or recreate), the helper's exit deleted the store. A volume named in
// --mount has an explicit source, so --rm never removes it.
//
// Anything that cannot be named — a volume with no name, a bind with no
// source, any other mount type — is refused rather than dropped: the helper
// must see exactly the filesystem the registry does.
func maintenanceMounts(info containerInfo) ([]string, error) {
	var args []string
	for _, m := range info.Mounts {
		var src string
		switch m.Type {
		case "volume":
			src = m.Name
		case "bind":
			src = m.Source
		default:
			return nil, fmt.Errorf("registry mount %s has type %q, which maintenance cannot reproduce", m.Destination, m.Type)
		}
		if src == "" || m.Destination == "" {
			return nil, fmt.Errorf("registry %s mount at %q has no explicit source", m.Type, m.Destination)
		}
		// --mount is CSV: a comma or quote in a value would re-split it.
		if strings.ContainsAny(src+m.Destination, `,"`) {
			return nil, fmt.Errorf("registry mount %q -> %q cannot be expressed as a --mount flag", src, m.Destination)
		}
		spec := "type=" + m.Type + ",src=" + src + ",dst=" + m.Destination
		if !m.RW {
			spec += ",readonly"
		}
		args = append(args, "--mount", spec)
	}
	return args, nil
}

func (r Runner) inspect(ctx context.Context, name string) (containerInfo, error) {
	b, err := r.docker(ctx, "inspect", name)
	if err != nil {
		return containerInfo{}, err
	}
	var result []containerInfo
	if err := json.Unmarshal(b, &result); err != nil {
		return containerInfo{}, err
	}
	if len(result) != 1 {
		return containerInfo{}, fmt.Errorf("expected one registry container")
	}
	return result[0], nil
}
func (r Runner) endpoint(ctx context.Context, name string) (string, error) {
	c, err := r.inspect(ctx, name)
	if err != nil {
		return "", err
	}
	ports := c.NetworkSettings.Ports["5000/tcp"]
	if len(ports) == 0 {
		return "", fmt.Errorf("registry %s has no published port", name)
	}
	return "http://127.0.0.1:" + ports[0].HostPort, nil
}

func registryRequest(ctx context.Context, base, path, method string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 20 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("registry %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func (r Runner) registryPlan(ctx context.Context, reg Registry, container, base string) ([]PlanEntry, error) {
	protected, err := r.protected(ctx, reg)
	if err != nil {
		return nil, err
	}
	tags, err := r.inventory(ctx, container)
	if err != nil {
		return nil, err
	}
	revisions, err := r.revisions(ctx, container)
	if err != nil {
		return nil, err
	}
	plan, err := registryGraphPlan(tags, revisions, r.Policy, reg, protected, time.Now(), func(v Version) ([]byte, error) {
		return registryRequest(ctx, base, "/v2/"+v.Repository+"/manifests/"+v.Digest, http.MethodGet)
	})
	if err != nil {
		return nil, err
	}
	var untagged, total uint64
	sized := true
	for _, e := range plan {
		total += e.Bytes
		sized = sized && e.Sized
		if e.Untagged {
			untagged++
			r.print("expire unreachable untagged manifest %s/%s@%s%s\n", reg.Container, e.Repository, e.Digest, sizeSuffix(e.Bytes, e.Sized))
			continue
		}
		r.print("expire tagged version %s/%s@%s (%s)%s\n", reg.Container, e.Repository, e.Digest, strings.Join(e.Tags, ", "), sizeSuffix(e.Bytes, e.Sized))
	}
	r.print("registry %s: %d eligible manifests (%d tagged versions, %d unreachable untagged); %d tags; retain %d days and %d versions\n", reg.Container, len(plan), uint64(len(plan))-untagged, untagged, len(tags), r.Policy.RegistryDays, r.Policy.RegistryKeep)
	if len(plan) > 0 {
		qualifier := "estimated reclaim"
		if !sized {
			qualifier = "estimated reclaim (partial: some manifests reported no sizes)"
		}
		r.print("registry %s: %s %s; layers shared with retained manifests are deduplicated, so the actual reclaim is lower\n", reg.Container, qualifier, formatBytes(total))
	}
	return plan, nil
}

func sizeSuffix(b uint64, sized bool) string {
	if !sized {
		return ""
	}
	return " (" + formatBytes(b) + ")"
}

func formatBytes(b uint64) string {
	switch {
	case b >= GiB:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(GiB))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(1<<20))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// manifestBody is the subset of an image manifest or index needed to walk the
// graph and estimate size. Config and layer sizes are the manifest's own
// accounting; an index carries neither, because its children hold the bytes.
// The json tags are the OCI/distribution wire names, which are lowercase — so
// unlike Docker's inspect output these are not merely explicit, they are the
// only spelling a reader could verify against the spec.
type manifestBody struct {
	Config struct {
		Size uint64 `json:"size"`
	} `json:"config"`
	Layers []struct {
		Size uint64 `json:"size"`
	} `json:"layers"`
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
}

// registryGraphPlan preserves the full graph of every retained manifest, not
// only tagged roots, and reclaims untagged manifests that nothing retained can
// reach. fetch errors abort the complete plan before deletion.
//
// Retained roots are retained tags, protected digests, and untagged revisions
// newer than the retention window; everything transitively reachable from them
// is retained too. A manifest outside that closure is eligible — which covers
// both an expired tagged version and the orphan a re-pushed tag left behind.
// This is graph-safe selection, and is why Distribution's own
// `garbage-collect --delete-untagged` must never be used: on 2.8.3 it deletes
// index children (distribution#3178).
func registryGraphPlan(tags []Tag, revisions []Revision, policy Policy, reg Registry, protected map[string]map[string]bool, now time.Time, fetch func(Version) ([]byte, error)) ([]PlanEntry, error) {
	expiredTag := map[Version]bool{}
	for _, v := range RegistryCandidates(tags, policy, reg, protected, now) {
		expiredTag[v] = true
	}
	tagged := map[Version][]string{}
	for _, t := range tags {
		v := Version{t.Repository, t.Digest}
		tagged[v] = append(tagged[v], t.Name)
	}
	for _, names := range tagged {
		sort.Strings(names)
	}

	ttl := time.Duration(policy.RegistryDays) * 24 * time.Hour
	var roots []Version
	// Retained tag roots are added even when the revision inventory does not list
	// them: an inconsistent inventory must fail closed on the manifest read
	// rather than silently lose protection.
	for v := range tagged {
		if !expiredTag[v] {
			roots = append(roots, v)
		}
	}
	// Protected digests root the graph wherever the registry HOLDS that
	// manifest. A bare digest names content, not a location, so it is tried in
	// every registered repository — but only kept where a revision link exists.
	//
	// That restriction is what keeps protection from aborting the plan, and it
	// loosens nothing. Much of the protected set is not a manifest in this
	// registry at all: a container run by image ID (RegistryGC's own retention
	// helper is started from the registry's image ID, a config digest), a
	// kubelet imageID from another registry, a release pin for a sibling
	// repository. Distribution resolves GET-by-digest through the very revision
	// link the inventory lists, so such a root 404s by construction, and one
	// 404 used to abort the whole plan. And since the deletion set below is
	// drawn only from tags and that same inventory, a digest the inventory does
	// not hold can never be deleted, so there is nothing for it to protect.
	//
	// Retained TAG roots and index children are deliberately NOT filtered this
	// way: those come from the registry's own metadata, so a missing manifest
	// there is an inconsistent inventory and must still fail closed.
	held := map[Version]bool{}
	for _, rev := range revisions {
		held[rev.Version] = true
	}
	for repo, refs := range protected {
		for ref := range refs {
			if !digestPattern.MatchString(ref) {
				continue
			}
			candidates := []string{repo}
			if repo == "*" {
				candidates = reg.Repositories
			}
			for _, candidate := range candidates {
				if v := (Version{candidate, ref}); held[v] {
					roots = append(roots, v)
				}
			}
		}
	}
	for _, rev := range revisions {
		if len(tagged[rev.Version]) > 0 {
			continue
		}
		// An untagged manifest inside the retention window is a root in its own
		// right: it may be an index or OCI artifact whose children must stay
		// pullable, and a push that is still in flight has no tag yet.
		if now.Sub(rev.Modified) <= ttl {
			roots = append(roots, rev.Version)
		}
	}

	retained := map[Version]bool{}
	for len(roots) > 0 {
		v := roots[len(roots)-1]
		roots = roots[:len(roots)-1]
		if retained[v] || !contains(reg.Repositories, v.Repository) {
			continue
		}
		retained[v] = true
		body, err := fetchManifest(fetch, v)
		if err != nil {
			return nil, err
		}
		for _, child := range body.Manifests {
			roots = append(roots, Version{v.Repository, child.Digest})
		}
	}

	var plan []PlanEntry
	seen := map[Version]bool{}
	eligible := func(v Version) error {
		if retained[v] || seen[v] || !contains(reg.Repositories, v.Repository) {
			return nil
		}
		seen[v] = true
		body, err := fetchManifest(fetch, v)
		if err != nil {
			return err
		}
		size := body.Config.Size
		for _, layer := range body.Layers {
			size += layer.Size
		}
		plan = append(plan, PlanEntry{Version: v, Untagged: len(tagged[v]) == 0, Tags: tagged[v], Bytes: size, Sized: true})
		return nil
	}
	for v := range expiredTag {
		if err := eligible(v); err != nil {
			return nil, err
		}
	}
	for _, rev := range revisions {
		if len(tagged[rev.Version]) > 0 || now.Sub(rev.Modified) <= ttl {
			continue
		}
		if err := eligible(rev.Version); err != nil {
			return nil, err
		}
	}
	sort.Slice(plan, func(i, j int) bool {
		if plan[i].Repository == plan[j].Repository {
			return plan[i].Digest < plan[j].Digest
		}
		return plan[i].Repository < plan[j].Repository
	})
	return plan, nil
}

func fetchManifest(fetch func(Version) ([]byte, error), v Version) (manifestBody, error) {
	var body manifestBody
	b, err := fetch(v)
	if err != nil {
		return body, err
	}
	if err := json.Unmarshal(b, &body); err != nil {
		return body, err
	}
	for _, child := range body.Manifests {
		if !digestPattern.MatchString(child.Digest) {
			return body, fmt.Errorf("invalid child digest")
		}
	}
	return body, nil
}

// Bringing a registry back after maintenance is retried: one failed docker
// call must not leave the registry every cluster pulls from offline. The
// attempts double as a readiness poll — a restarted registry takes a moment
// to answer — and are bounded by restoreBudget. restoreRetryDelay is a
// variable so tests do not sleep.
const (
	defaultRestoreRetryDelay = 2 * time.Second
	restoreAttempts          = 15
	restoreBudget            = 2 * time.Minute
)

var restoreRetryDelay = defaultRestoreRetryDelay

// registryStopMarker is where a pass records that it is about to stop a
// registry, beside the policy so it outlives the process. A pass that dies
// between the stop and the restart (SIGKILL, logout, Docker Desktop quitting)
// leaves the registry stopped, and an explicit stop disables its
// unless-stopped restart policy, so nothing else will bring it back. The
// marker is how the next pass tells "forge stopped this and never finished"
// from "someone stopped this on purpose".
func registryStopMarker(policyPath, container string) string {
	return filepath.Join(filepath.Dir(policyPath), "registry-maintenance", container+".stopped")
}

// markRegistryStopped records the intent to stop container. It is written
// BEFORE the stop: a registry forge cannot account for is never stopped.
func (r Runner) markRegistryStopped(container string) error {
	if r.PolicyPath == "" {
		return fmt.Errorf("refusing to stop registry %s: no policy path to record the stop beside, so an interrupted pass could not restart it", container)
	}
	if err := guardMachinePolicy(r.PolicyPath); err != nil {
		return err
	}
	path := registryStopMarker(r.PolicyPath, container)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(struct {
		Container string    `json:"container"`
		StoppedAt time.Time `json:"stopped_at"`
		PID       int       `json:"pid"`
	}{container, time.Now(), os.Getpid()})
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(b, '\n'), 0o600)
}

func (r Runner) registryMarkedStopped(container string) bool {
	if r.PolicyPath == "" {
		return false
	}
	_, err := os.Stat(registryStopMarker(r.PolicyPath, container))
	return err == nil
}

func (r Runner) clearRegistryStopped(container string) error {
	if r.PolicyPath == "" {
		return nil
	}
	err := os.Remove(registryStopMarker(r.PolicyPath, container))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// restoreRegistry brings a registry forge stopped back into service, and only
// reports success once it is running and answering. Before the registry is
// started, every maintenance writer must be confirmed gone: a garbage-collect
// sweep running against the store while the registry accepts pushes can
// delete the blobs of an image pushed during the sweep. So a writer that
// cannot be confirmed gone keeps the registry stopped — with the marker in
// place, so the next pass tries again — and that is reported as an error.
//
// It runs on its own context: the caller's may already be cancelled, which
// is exactly the case (SIGTERM mid-pass) this has to survive.
func (r Runner) restoreRegistry(container string, writers ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), restoreBudget)
	defer cancel()
	var last error
	for attempt := 0; attempt < restoreAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				last = errors.Join(last, ctx.Err())
				attempt = restoreAttempts
				continue
			case <-time.After(restoreRetryDelay):
			}
		}
		if last = r.stopMaintenanceWriters(ctx, writers); last != nil {
			continue
		}
		if _, last = r.docker(ctx, "start", container); last != nil {
			continue
		}
		if last = r.registryServing(ctx, container); last != nil {
			continue
		}
		if err := r.clearRegistryStopped(container); err != nil {
			r.print("registry %s restarted, but its maintenance marker could not be cleared: %v\n", container, err)
		}
		return nil
	}
	return fmt.Errorf("registry %s is STOPPED and could not be restarted after maintenance: %w; "+
		"the next `forge storage gc` retries, or run `docker start %s` once no %s-forge-* container is running",
		container, last, container, container)
}

// stopMaintenanceWriters stops every named maintenance container that exists.
func (r Runner) stopMaintenanceWriters(ctx context.Context, writers []string) error {
	for _, name := range writers {
		b, err := r.docker(ctx, "ps", "-aq", "--filter", "name=^/"+name+"$")
		if err != nil {
			return fmt.Errorf("confirm maintenance writer %s is gone: %w", name, err)
		}
		if strings.TrimSpace(string(b)) != "" {
			if _, err := r.docker(ctx, "stop", name); err != nil {
				return fmt.Errorf("stop maintenance writer %s: %w", name, err)
			}
		}
	}
	return nil
}

// registryServing verifies a started registry is actually up: `docker start`
// exiting 0 says nothing about a registry that crashes a moment later.
func (r Runner) registryServing(ctx context.Context, container string) error {
	info, err := r.inspect(ctx, container)
	if err != nil {
		return err
	}
	if !info.State.Running {
		return fmt.Errorf("registry %s is not running after start", container)
	}
	base, err := r.endpoint(ctx, container)
	if err != nil {
		return err
	}
	_, err = registryRequest(ctx, base, "/v2/", http.MethodGet)
	return err
}

// RegistryGC deletes eligible manifests and collects blobs with all writers stopped.
func (r Runner) RegistryGC(ctx context.Context, reg Registry, apply bool) (err error) {
	info, err := r.inspect(ctx, reg.Container)
	if err != nil {
		if strings.Contains(err.Error(), "No such object") {
			r.print("registry %s no longer exists; skipping\n", reg.Container)
			return nil
		}
		return err
	}
	helper, gc := reg.Container+"-forge-retention", reg.Container+"-forge-gc"
	if !info.State.Running {
		if !r.registryMarkedStopped(reg.Container) {
			r.print("registry %s stopped; skipping\n", reg.Container)
			return nil
		}
		// An earlier pass stopped it and never brought it back. That is a
		// repair, not a cleanup, so it happens on a preview too.
		r.print("registry %s was left stopped by an interrupted maintenance pass; restarting it\n", reg.Container)
		if err := r.restoreRegistry(reg.Container, gc, helper); err != nil {
			return err
		}
		r.print("registry %s restarted; retention runs on the next pass\n", reg.Container)
		return nil
	}
	// Running, so any marker is stale (restarted by hand, or by a pass that
	// could not clear it). Left in place it would make forge restart this
	// registry the next time someone stops it on purpose.
	if err := r.clearRegistryStopped(reg.Container); err != nil {
		return err
	}
	volume := false
	for _, m := range info.Mounts {
		if m.Type == "volume" && m.Destination == "/var/lib/registry" {
			volume = true
		}
	}
	if !volume || len(info.Config.Cmd) != 1 || info.Config.Cmd[0] != "/etc/docker/registry/config.yml" {
		return fmt.Errorf("registry %s is not a supported standalone k3d filesystem registry", reg.Container)
	}
	mounts, err := maintenanceMounts(info)
	if err != nil {
		return fmt.Errorf("registry %s: %w", reg.Container, err)
	}
	if err := r.verifyConsumers(ctx, reg, info); err != nil {
		return err
	}
	var b []byte

	base, err := r.endpoint(ctx, reg.Container)
	if err != nil {
		return err
	}
	plan, err := r.registryPlan(ctx, reg, reg.Container, base)
	if err != nil {
		return err
	}
	needsDelete, err := r.registryNeedsDelete(ctx, reg.Container, info)
	if err != nil {
		return err
	}
	if needsDelete && len(plan) > 0 {
		r.print("registry %s: manifest delete is not enabled (it predates k3d --delete-enabled); %s will enable it in config.yml inside the maintenance window, once\n", reg.Container, applyVerb(apply))
	}
	if !apply || len(plan) == 0 {
		return nil
	}
	if err := r.refuseStaleMaintenance(ctx, helper, gc); err != nil {
		return err
	}
	if err := r.markRegistryStopped(reg.Container); err != nil {
		return err
	}
	// Registered before the stop, so it runs whatever happens next — a failed
	// stop, a failed helper, a cancelled context. Only a dead process skips
	// it, and the marker written above covers that.
	migrated := false
	defer func() {
		if restoreErr := r.restoreRegistry(reg.Container, gc, helper); restoreErr != nil {
			err = errors.Join(err, restoreErr)
			return
		}
		if migrated {
			if verifyErr := r.verifyRegistryDelete(context.Background(), reg.Container, reg.Repositories[0]); verifyErr != nil {
				err = errors.Join(err, verifyErr)
				return
			}
			r.print("registry %s: manifest delete enabled and verified\n", reg.Container)
		}
	}()
	if _, err = r.docker(ctx, "stop", "--time", "30", reg.Container); err != nil {
		return err
	}
	if needsDelete {
		// A failure here must not strand the pass: the helper below deletes
		// through its own delete-enabled environment, so this pass still
		// reclaims, and the next one retries the migration.
		if err := r.enableRegistryDelete(ctx, reg.Container); err != nil {
			r.print("registry %s: could not enable manifest delete in config.yml (%v); retention still runs this pass\n", reg.Container, err)
		} else {
			migrated = true
		}
	}
	base, err = r.startRetentionHelper(ctx, helper, mounts, info.Image)
	if err != nil {
		return err
	}
	plan, err = r.registryPlan(ctx, reg, helper, base)
	if err != nil {
		return err
	}
	for _, v := range plan {
		if _, err = registryRequest(ctx, base, "/v2/"+v.Repository+"/manifests/"+v.Digest, http.MethodDelete); err != nil {
			return err
		}
	}
	if _, err = r.docker(ctx, "stop", helper); err != nil {
		return err
	}
	// Never --delete-untagged: on 2.8.3 it deletes index children
	// (distribution#3178). Unreachable untagged manifests were already selected
	// by the graph walk and DELETEd above, so this pass only reclaims blobs that
	// no remaining manifest references.
	gcArgs := append([]string{"run", "--rm", "--pull=never", "--network", "none", "--name", gc}, mounts...)
	gcArgs = append(gcArgs, info.Image, "garbage-collect", "/etc/docker/registry/config.yml")
	b, err = r.docker(ctx, gcArgs...)
	r.print("%s\n", b)
	return err
}

// refuseStaleMaintenance fails when a maintenance container from an earlier
// pass still exists: it may hold the registry's volume, and its state is the
// evidence of what went wrong, so it is left for a human to inspect.
func (r Runner) refuseStaleMaintenance(ctx context.Context, names ...string) error {
	for _, name := range names {
		b, err := r.docker(ctx, "ps", "-aq", "--filter", "name=^/"+name+"$")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) != "" {
			return fmt.Errorf("stale maintenance container %s: inspect before retrying", name)
		}
	}
	return nil
}

// startRetentionHelper runs the delete-enabled registry the retention pass
// talks to, on the stopped registry's own storage, and returns its endpoint
// once it answers /v2/ (30 one-second attempts; the last error otherwise).
func (r Runner) startRetentionHelper(ctx context.Context, helper string, mounts []string, image string) (string, error) {
	// Never --volumes-from with --rm: see maintenanceMounts.
	args := append([]string{"run", "-d", "--rm", "--pull=never", "--name", helper}, mounts...)
	args = append(args, "-p", "127.0.0.1::5000", "-e", "REGISTRY_STORAGE_DELETE_ENABLED=true", image)
	if _, err := r.docker(ctx, args...); err != nil {
		return "", err
	}
	base, err := r.endpoint(ctx, helper)
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 30; attempt++ {
		if _, err = registryRequest(ctx, base, "/v2/", http.MethodGet); err == nil {
			return base, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return "", err
}

func applyVerb(apply bool) string {
	if apply {
		return "this pass"
	}
	return "an apply"
}

const registryConfigPath = "/etc/docker/registry/config.yml"

// registryNeedsDelete reports whether a running registry refuses manifest
// deletes: neither its config.yml nor its environment enables them. A
// registry created before forge passed --delete-enabled to k3d is in this
// state, and until it is migrated retention can only restart the container
// behind a delete-enabled config on every pass.
func (r Runner) registryNeedsDelete(ctx context.Context, container string, info containerInfo) (bool, error) {
	for _, e := range info.Config.Env {
		if strings.EqualFold(e, "REGISTRY_STORAGE_DELETE_ENABLED=true") {
			return false, nil
		}
	}
	b, err := r.docker(ctx, "exec", container, "cat", registryConfigPath)
	if err != nil {
		return false, err
	}
	enabled, err := configDeleteEnabled(b)
	return !enabled, err
}

func configDeleteEnabled(config []byte) (bool, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(config, &doc); err != nil {
		return false, fmt.Errorf("parse registry config: %w", err)
	}
	storage, _ := doc["storage"].(map[string]any)
	del, _ := storage["delete"].(map[string]any)
	enabled, _ := del["enabled"].(bool)
	return enabled, nil
}

// withDeleteEnabled returns config with storage.delete.enabled set to true and
// everything else preserved (comments aside).
func withDeleteEnabled(config []byte) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(config, &doc); err != nil {
		return nil, fmt.Errorf("parse registry config: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("registry config is empty")
	}
	storage, ok := doc["storage"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("registry config has no storage section")
	}
	storage["delete"] = map[string]any{"enabled": true}
	return yaml.Marshal(doc)
}

// enableRegistryDelete rewrites config.yml inside the STOPPED registry
// container, so the edit rides the restart that maintenance performs anyway.
// It lives in the container's writable layer: it survives restarts and Docker
// Desktop upgrades, and a recreate takes it back to whatever k3d was told —
// which forge now tells to enable delete. Recreating instead was rejected: an
// anonymous data volume dies with a `k3d registry delete`, and the container
// id, labels and network aliases clusters resolve through would all be rebuilt.
func (r Runner) enableRegistryDelete(ctx context.Context, container string) error {
	dir, err := os.MkdirTemp("", "forge-registry-config-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "config.yml")
	if _, err := r.docker(ctx, "cp", container+":"+registryConfigPath, file); err != nil {
		return err
	}
	current, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	next, err := withDeleteEnabled(current)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(file, next, 0o644); err != nil {
		return err
	}
	_, err = r.docker(ctx, "cp", file, container+":"+registryConfigPath)
	return err
}

// verifyRegistryDelete asks the running registry to delete a manifest that
// cannot exist. A delete-enabled distribution answers 404 (unknown manifest);
// one that refuses the verb answers 405. It deletes nothing either way.
func (r Runner) verifyRegistryDelete(ctx context.Context, container, repository string) error {
	base, err := r.endpoint(ctx, container)
	if err != nil {
		return err
	}
	_, err = registryRequest(ctx, base, "/v2/"+repository+"/manifests/sha256:"+strings.Repeat("0", 64), http.MethodDelete)
	if err == nil || strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), "HTTP 202") {
		return nil
	}
	return fmt.Errorf("registry %s still refuses manifest delete after migration: %w", container, err)
}

func (r Runner) containerReferences(ctx context.Context) ([]string, error) {
	var refs []string
	b, err := r.docker(ctx, "ps", "-aq")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(b))
	if len(ids) == 0 {
		return nil, nil
	}
	chunks, err := r.inspectContainers(ctx, ids)
	if err != nil {
		return nil, err
	}
	containers, err := decodeInspected[struct {
		Config struct{ Image string }
		Image  string
	}](chunks)
	if err != nil {
		return nil, err
	}
	var images []string
	seen := map[string]bool{}
	for _, c := range containers {
		refs = append(refs, c.Config.Image)
		if !seen[c.Image] {
			images = append(images, c.Image)
			seen[c.Image] = true
		}
	}
	if len(images) > 0 {
		b, err = r.docker(ctx, append([]string{"image", "inspect"}, images...)...)
		if err != nil {
			return nil, err
		}
		var inspected []struct{ RepoDigests []string }
		if err := json.Unmarshal(b, &inspected); err != nil {
			return nil, err
		}
		for _, im := range inspected {
			refs = append(refs, im.RepoDigests...)
		}
	}
	return refs, nil
}

func (r Runner) verifyConsumers(ctx context.Context, reg Registry, info containerInfo) error {
	// Every connected k3d cluster must be checked, including stopped clusters;
	// sharing the registry cannot silently introduce an unprotected consumer.
	b, err := r.docker(ctx, "ps", "-aq", "--filter", "label=k3d.role=server")
	if err != nil {
		return err
	}
	if ids := strings.Fields(string(b)); len(ids) > 0 {
		chunks, err := r.inspectContainers(ctx, ids)
		if err != nil {
			return err
		}
		nodes, err := decodeInspected[struct {
			Config          struct{ Labels map[string]string }
			NetworkSettings struct{ Networks map[string]any }
		}](chunks)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			for network := range n.NetworkSettings.Networks {
				if _, shared := info.NetworkSettings.Networks[network]; shared {
					cluster := "k3d-" + n.Config.Labels["k3d.cluster"]
					if !contains(reg.Contexts, cluster) {
						return fmt.Errorf("registry %s also serves %s; register this context before cleanup", reg.Container, cluster)
					}
				}
			}
		}
	}
	return nil
}
