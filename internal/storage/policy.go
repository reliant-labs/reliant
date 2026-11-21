// Package storage owns host storage budgets and the lifecycle of explicitly
// registered local development caches. Persistent application data is never GC'd.
//
//forge:exclude-contract: local CLI maintenance with explicit policy and process adapters, not an injected application component
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// GiB is one gibibyte in bytes.
const GiB = uint64(1 << 30)

// Policy defines one machine’s cache budgets and protected references.
type Policy struct {
	Projects         []string `json:"projects,omitempty"`
	LogBudgetGiB     uint64   `json:"log_budget_gib"`
	HostReserveGiB   uint64   `json:"host_reserve_gib"`
	HostPaths        []string `json:"host_paths,omitempty"`
	BuildCacheGiB    uint64   `json:"build_cache_gib"`
	BuildCacheUnused string   `json:"build_cache_unused"`
	ImageUnused      string   `json:"image_unused"`
	// GoCacheUnused is how long a shared Go build cache entry (and the
	// golangci-lint cache) may sit unused before it is trimmed. Minimum 2h:
	// Go refreshes an entry's mtime on use once it is an hour stale, so that
	// floor is what keeps the trim safe under running builds.
	GoCacheUnused string `json:"go_cache_unused"`
	// GoCacheGiB is the size the shared Go caches are trimmed down to,
	// oldest first, when age alone leaves them larger.
	GoCacheGiB uint64 `json:"go_cache_gib"`
	// GoCacheVolumePercent caps that budget at this share of the volume the
	// cache lives on, so the default works on a 40 GiB build volume as well as
	// on a 1 TB laptop: the budget is the smaller of go_cache_gib and this
	// share. Zero means the default (15); 100 leaves go_cache_gib alone.
	GoCacheVolumePercent int `json:"go_cache_volume_percent,omitempty"`
	// GolangciCacheGiB is the size the golangci-lint cache is trimmed down
	// to, least recently used first, like go_cache_gib for the build cache
	// and capped the same way. Its own budget because its entries are tiny
	// and recomputed in seconds: under go_cache_gib it was never trimmed.
	GolangciCacheGiB uint64 `json:"golangci_cache_gib"`
	// SourceCacheUnused is how long a cross-repo source clone may go
	// unresolved before it becomes an eviction candidate. Longer than the
	// image and build-cache windows because the cost of being wrong is a
	// full re-clone of a repository, not a re-pull of a layer.
	SourceCacheUnused string `json:"source_cache_unused"`
	// SourceCacheKeep is how many of the most recently used clones of one
	// repository are retained regardless of age. This is the floor that
	// keeps an idle project's current pin warm, so it is validated at 1 or
	// more: a policy that can empty a repository's only pin is not a policy
	// anyone wants by accident.
	SourceCacheKeep int `json:"source_cache_keep"`
	// SourceCacheGiB is the size the source cache is evicted down to, least
	// recently used clone first, never touching the newest source_cache_keep
	// clones of a repository. The age rule alone does not bound it: a project
	// that bumps its pin every few hours produces more clones in
	// source_cache_unused than any age can hold.
	SourceCacheGiB uint64 `json:"source_cache_gib"`
	// SourceCacheSlimAfter is how long a clone beyond the newest
	// source_cache_keep of its repository may sit unused before its installed
	// dependencies (node_modules) are removed. Those are 80% of a clone's
	// bytes and a build reinstalls them (frontenddeps.Ensure); the pristine
	// checkout stays.
	SourceCacheSlimAfter string     `json:"source_cache_slim_after"`
	RegistryDays         int        `json:"registry_days"`
	RegistryKeep         int        `json:"registry_keep"`
	DockerContext        string     `json:"docker_context,omitempty"`
	Builders             []string   `json:"builders"`
	Registries           []Registry `json:"registries,omitempty"`
	Clusters             []string   `json:"clusters,omitempty"`
	Pins                 []string   `json:"pins,omitempty"`

	// Repos are git repositories whose linked worktrees the worktree layer
	// reclaims, beyond the repository of each registered project: sibling
	// repos a project builds from (docker.build_contexts, go.mod replaces).
	// Filled by Converge; git itself enumerates the worktrees, wherever they live.
	Repos []string `json:"repos,omitempty"`
	// WorktreeRebuildable replaces the built-in allowlist of ignored paths a
	// removable worktree may contain. Nil means the defaults.
	WorktreeRebuildable []string `json:"worktree_rebuildable,omitempty"`
	// WorktreeReap lets an explicit `forge storage gc --apply` remove
	// worktrees. Off, the layer only reports. No automatic pass ever removes
	// one, whatever this says (Runner.Explicit).
	WorktreeReap bool `json:"worktree_reap,omitempty"`
	// WorktreeReapReliantManaged is ignored. It once extended worktree_reap to
	// worktrees under Reliant's worktree root, which were held by location;
	// they are now held only when Reliant has locked them (HoldManaged), so
	// nothing is left for it to widen. Kept so a policy that sets it loads.
	WorktreeReapReliantManaged bool `json:"worktree_reap_reliant_managed,omitempty"`

	// MaxDeletesPerPass bounds how many filesystem entries one maintenance
	// pass may unlink, across every layer. What is left is retained and the
	// next pass continues, so a budget is reached over several passes rather
	// than by one burst of six-figure unlinks. Zero means the default.
	MaxDeletesPerPass int `json:"max_deletes_per_pass"`
	// DeleteRatePerSec paces those unlinks. Zero means the default.
	DeleteRatePerSec int `json:"delete_rate_per_sec"`

	// AnonymousVolumeUnused is how long forge must have seen an anonymous
	// Docker volume referenced by no container, running or stopped, before
	// an explicit `forge storage gc --apply` may remove it (anonvolumes.go).
	// Minimum 24h; "off" disables the layer. Named volumes are never removed,
	// whatever this says.
	AnonymousVolumeUnused string `json:"anonymous_volume_unused"`
	// MaxDockerRemovalsPerPass bounds how many Docker images and volumes one
	// pass asks the daemon to remove. Zero means the default.
	MaxDockerRemovalsPerPass int `json:"max_docker_removals_per_pass"`
}

// anonymousVolumesOff is the AnonymousVolumeUnused value that turns the
// anonymous-volume layer off entirely: no observation, no removal.
const anonymousVolumesOff = "off"

// minAnonymousVolumeUnused is the shortest unused window a policy may set. A
// volume holds data a container wrote, not a rebuildable artifact, so the
// window is a recovery period after the last container using it went away.
const minAnonymousVolumeUnused = 24 * time.Hour

// Registry explicitly identifies a local registry and all of its consumers.
type Registry struct {
	Container    string   `json:"container"`
	Repositories []string `json:"repositories"`
	Aliases      []string `json:"aliases"`
	Contexts     []string `json:"contexts"`
}

// DefaultPolicy returns conservative local development budgets.
func DefaultPolicy() Policy {
	return Policy{LogBudgetGiB: 1, HostReserveGiB: 20, BuildCacheGiB: 20, BuildCacheUnused: "168h", GoCacheUnused: "48h", GoCacheGiB: 60, GolangciCacheGiB: 4, ImageUnused: "168h", SourceCacheUnused: "168h", SourceCacheKeep: 2, SourceCacheGiB: 6, SourceCacheSlimAfter: "24h", RegistryDays: 14, RegistryKeep: 5, Builders: []string{"default"},
		MaxDeletesPerPass: defaultMaxDeletesPerPass, DeleteRatePerSec: defaultDeleteRatePerSec,
		AnonymousVolumeUnused: "336h", MaxDockerRemovalsPerPass: defaultMaxDockerRemovalsPerPass}
}

// DefaultPath resolves the machine policy location.
func DefaultPath() (string, error) {
	if path := os.Getenv("FORGE_STORAGE_POLICY"); path != "" {
		return filepath.Abs(path)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "forge", "storage.json"), nil
}

// Load overlays a JSON document on the default policy, tolerating unknown keys.
func Load(path string) (Policy, error) {
	p := DefaultPolicy()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("storage policy: %w", err)
	}
	warnUnknownPolicyKeys(path, b)
	return p, p.Validate()
}

// PolicyWarnings receives the notice that storage.json holds keys this build
// does not know (written by a newer or older forge, or misspelled). Per-machine
// state is shared across forge versions, so an unknown key must never fail an
// unrelated command; strictness belongs to forge.yaml. Unknown keys fall back to
// defaults here but are preserved on Save for other versions. Warned once per path.
var PolicyWarnings io.Writer = os.Stderr

var warnedPolicyPaths sync.Map

// retiredPolicyKeys are keys an older forge wrote and this one no longer
// reads: the module-cache bound and the tool-cache eviction, both removed when
// forge stopped managing the Go module cache and its own tool installs. A
// machine's storage.json keeps them (Save preserves unknown keys, so an older
// forge sharing the file still sees its settings), and they are not worth a
// warning on every command.
var retiredPolicyKeys = map[string]bool{
	"go_mod_cache_gib":  true,
	"tool_cache_unused": true,
	"tool_cache_keep":   true,
}

func warnUnknownPolicyKeys(path string, doc []byte) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(doc, &raw) != nil {
		return
	}
	known := map[string]bool{}
	t := reflect.TypeOf(Policy{})
	for i := 0; i < t.NumField(); i++ {
		known[strings.Split(t.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	var unknown []string
	for k := range raw {
		if !known[k] && !retiredPolicyKeys[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return
	}
	if _, seen := warnedPolicyPaths.LoadOrStore(path, true); seen {
		return
	}
	sort.Strings(unknown)
	fmt.Fprintf(PolicyWarnings, "warning: %s has keys this forge does not recognise (%s); ignoring them (written by a different forge version, or misspelled?)\n", path, strings.Join(unknown, ", "))
}

// Validate rejects unbounded budgets and unscoped registry targets.
func (p Policy) Validate() error {
	if p.LogBudgetGiB == 0 || p.HostReserveGiB == 0 || p.BuildCacheGiB == 0 || p.GoCacheGiB == 0 || p.GolangciCacheGiB == 0 || p.SourceCacheGiB == 0 || p.RegistryDays < 1 || p.RegistryKeep < 2 {
		return fmt.Errorf("storage: positive budgets and retention, and at least two registry versions are required")
	}
	if p.SourceCacheKeep < 1 {
		return fmt.Errorf("storage: source_cache_keep must retain at least one clone per repository, got %d", p.SourceCacheKeep)
	}
	if p.GoCacheVolumePercent < 0 || p.GoCacheVolumePercent > 100 {
		return fmt.Errorf("storage: go_cache_volume_percent must be 1-100 (0 means the default %d), got %d", defaultGoCacheVolumePercent, p.GoCacheVolumePercent)
	}
	if p.MaxDeletesPerPass < 0 || p.DeleteRatePerSec < 0 || p.MaxDockerRemovalsPerPass < 0 {
		return fmt.Errorf("storage: max_deletes_per_pass, delete_rate_per_sec and max_docker_removals_per_pass must be positive (0 means the default), got %d, %d and %d", p.MaxDeletesPerPass, p.DeleteRatePerSec, p.MaxDockerRemovalsPerPass)
	}
	if _, err := p.anonymousVolumeUnused(); err != nil {
		return err
	}
	for _, v := range []string{p.ImageUnused, p.BuildCacheUnused, p.SourceCacheUnused, p.SourceCacheSlimAfter, p.GoCacheUnused} {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Hour {
			return fmt.Errorf("storage: invalid unused duration %q (minimum 1h)", v)
		}
	}
	for _, r := range p.Registries {
		if !strings.HasPrefix(r.Container, "k3d-") || len(r.Contexts) == 0 || len(r.Repositories) == 0 || len(r.Aliases) == 0 {
			return fmt.Errorf("registry %q requires explicit repositories, aliases, and protecting cluster contexts", r.Container)
		}
		for _, c := range r.Contexts {
			if !strings.HasPrefix(c, "k3d-") {
				return fmt.Errorf("registry retention only accepts local k3d contexts: %s", c)
			}
		}
	}
	return nil
}

// ErrMachinePolicyUnderTest refuses a write to the developer's real machine
// policy from inside `go test`. Callers on a warn-never-fail path (the
// zero-touch converge hooks, builder registration, release pinning) treat it
// as "skip": a test has nothing real to register.
var ErrMachinePolicyUnderTest = errors.New("storage: refusing to write the machine storage policy under test " +
	"(set FORGE_STORAGE_POLICY to a t.TempDir() path to exercise policy writes)")

// guardMachinePolicy refuses path when it is the machine-default policy and
// the process is a test binary that did not name it explicitly.
//
// The zero-touch activation (converge.go) makes `forge build`, a release cut
// and the cluster phase WRITE the policy as a side effect. Under `go test`
// those paths are exercised constantly by tests that are about something else
// entirely, and each one wrote the developer's real storage.json — measured:
// 65 leaked `TestBuildTag_*/001` temp projects and fake pins in one machine's
// policy, and Policy.Projects is the set the Logs layer expires files under.
// This is the same hole TempRoot and SourceCacheRoot close (tempsweep.go,
// sources.go), closed the same way: the default is unreachable from a test, so
// a silent write becomes a message naming what to set.
//
// FORGE_STORAGE_POLICY set explicitly by a test is a deliberate scope and is
// honoured — DefaultPath returns it, so it is never "the default" here. The
// value the process INHERITED is not: a Reliant agent's shell exports the
// machine's real policy there, and honouring it let `task test` on such a host
// write fake pins into it and queue every release-cut test behind the host's
// own maintenance pass for minutes. Reads are not guarded: loading the policy
// cannot change the machine.
func guardMachinePolicy(path string) error {
	if !testing.Testing() {
		return nil
	}
	if inheritedPolicyPath != "" && samePath(path, inheritedPolicyPath) {
		return ErrMachinePolicyUnderTest
	}
	if os.Getenv("FORGE_STORAGE_POLICY") != "" {
		return nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		// No resolvable machine policy location means path cannot be it.
		return nil
	}
	if samePath(path, filepath.Join(dir, "forge", "storage.json")) {
		return ErrMachinePolicyUnderTest
	}
	return nil
}

// inheritedPolicyPath is FORGE_STORAGE_POLICY as the process started with it,
// read before any test can t.Setenv it: under `go test` that is the host's
// policy, never a fixture (guardMachinePolicy).
var inheritedPolicyPath = os.Getenv("FORGE_STORAGE_POLICY")

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(absA) == filepath.Clean(absB)
}

// marshalPolicyPreservingUnknown keeps settings owned by other Forge versions.
// Read at save time (not only Load): registration can construct a fresh Policy,
// and must not silently reset retention understood by an older running daemon.
// Callers serialize read-modify-write through WithLock; atomic rename alone is
// not a transaction with writers that do not participate in that lock.
func marshalPolicyPreservingUnknown(path string, p Policy) ([]byte, error) {
	known, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var next map[string]json.RawMessage
	if err := json.Unmarshal(known, &next); err != nil {
		return nil, err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read existing storage policy: %w", err)
	}
	if err == nil {
		var previous map[string]json.RawMessage
		if err := json.Unmarshal(existing, &previous); err != nil || previous == nil {
			return nil, fmt.Errorf("storage: cannot preserve settings from invalid policy %s", path)
		}
		// Include omitempty fields in the known set: their deliberate removal
		// must not resurrect them from the previous document.
		typ := reflect.TypeOf(p)
		knownKeys := make(map[string]bool, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			knownKeys[strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]] = true
		}
		for key, value := range previous {
			if !knownKeys[key] {
				next[key] = value
			}
		}
	}
	return json.MarshalIndent(next, "", "  ")
}

// Save atomically replaces a validated policy, preserving unknown settings.
// Read-modify-write callers must hold WithLock across their Load and Save.
func Save(path string, p Policy) error {
	if err := guardMachinePolicy(path); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := marshalPolicyPreservingUnknown(path, p)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".storage-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Disk describes physical filesystem capacity.
type Disk struct {
	Path      string `json:"path"`
	Available uint64 `json:"available_bytes"`
	Capacity  uint64 `json:"capacity_bytes"`
}

// ReserveError is a MEASURED shortfall: the filesystem under Path was read and
// holds less than the policy's host reserve. It is a distinct type because
// it is the one CheckSpace outcome a caller may knowingly accept — an
// ephemeral CI runner, whose disk is discarded with the job — while an
// unreadable disk or path is an unknown that no caller should wave through.
type ReserveError struct {
	Path       string
	Available  uint64
	ReserveGiB uint64
}

func (e *ReserveError) Error() string {
	return fmt.Sprintf("host disk %s has %.1f GiB free, below the %d GiB reserve; run 'forge storage status' and 'forge storage gc --apply' before building (persistent volumes are never automatically deleted)", e.Path, float64(e.Available)/float64(GiB), e.ReserveGiB)
}

// CheckSpace refuses builds that would start below the physical host reserve.
// A shortfall is returned as a *ReserveError; any other error means a disk
// could not be measured at all.
func CheckSpace(p Policy, paths ...string) ([]Disk, error) {
	paths = append(paths, p.HostPaths...)
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	paths = append(paths, home)
	var disks []Disk
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		existing, err := existingStoragePath(path)
		if err != nil {
			return disks, fmt.Errorf("host disk %s: %w", path, err)
		}
		d, err := DiskSpace(existing)
		if err != nil {
			return disks, fmt.Errorf("host disk %s: %w", path, err)
		}
		d.Path = path
		disks = append(disks, d)
		if d.Available < p.HostReserveGiB*GiB {
			return disks, &ReserveError{Path: path, Available: d.Available, ReserveGiB: p.HostReserveGiB}
		}
	}
	return disks, nil
}

// KubeletConfig relies on kubelet's own usage tracking and GC, never on deleting
// containerd files underneath it. Unused age is independent of VM disk fullness.
func (p Policy) KubeletConfig() []byte {
	b, _ := json.MarshalIndent(map[string]any{"apiVersion": "kubelet.config.k8s.io/v1beta1", "kind": "KubeletConfiguration", "imageMaximumGCAge": p.ImageUnused, "imageMinimumGCAge": "10m", "imageGCHighThresholdPercent": 80, "imageGCLowThresholdPercent": 70, "containerLogMaxSize": "10Mi", "containerLogMaxFiles": 3}, "", "  ")
	return b
}

// CheckBuildSpace includes build scratch and explicitly configured Go caches,
// which can live on different filesystems from the project and home directory.
func CheckBuildSpace(p Policy, paths ...string) ([]Disk, error) {
	paths = append(paths, os.TempDir())
	for _, name := range []string{"GOTMPDIR", "GOCACHE", "GOMODCACHE"} {
		if path := os.Getenv(name); path != "" && path != "off" {
			paths = append(paths, path)
		}
	}
	return CheckSpace(p, paths...)
}

// A new output/cache directory consumes the filesystem of its nearest existing
// parent. Check that filesystem before the build creates the directory.
func existingStoragePath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("no existing parent for %s", path)
		}
		path = parent
	}
}
