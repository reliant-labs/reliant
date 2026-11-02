// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/dockerengine"
)

const (
	// ConsumerScanInterval is how often a volume under pressure is
	// re-measured. Measuring walks the cache directories (a 7 GB module cache
	// is ~300k entries), so it runs only while a volume is under pressure, at
	// once when pressure asserts, and then no more often than this.
	ConsumerScanInterval = 5 * time.Minute
	// consumerScanBudget bounds one whole scan; dirBudget and dirMaxEntries
	// bound one directory. A walk cut short reports a lower bound.
	consumerScanBudget = 90 * time.Second
	dirBudget          = 15 * time.Second
	dirMaxEntries      = 1_000_000
	// maxConsumers caps what a volume reports, biggest first.
	maxConsumers = 12
	// GoCacheTrimAge is the age past which a Go-format cache entry counts as
	// reclaimable in the breakdown (an estimate; forge's reaper decides what it
	// actually removes, reaper.go). Go bumps an entry's mtime on use once it
	// is an hour stale, so an entry untouched for two hours is not in use by a
	// live build — the floor forge's own storage GC uses, for the same reason.
	GoCacheTrimAge = 2 * time.Hour
)

// dirMeasure is one directory's measurement.
type dirMeasure struct {
	bytes       uint64
	reclaimable uint64
	approx      bool
}

// isShard reports whether name is one of a Go-format cache's two-hex-digit
// shard directories ("00".."ff"). Only entries inside them are cache entries;
// README and trim.txt at the top level are bookkeeping and never touched.
func isShard(name string) bool {
	if len(name) != 2 {
		return false
	}
	for _, c := range name {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// isGoFormatCache reports whether a consumer kind uses the Go build cache's
// layout (shard dirs of content-addressed entries).
func isGoFormatCache(kind string) bool {
	return kind == KindGoBuildCache || kind == KindGolangciLintCache
}

// measureDir sums the allocated size of everything under root (du's measure)
// without leaving root's filesystem or descending into skip (nested consumers
// measured on their own). For a Go-format cache it also sums the entries
// untouched since cutoff: what a trim would free.
func measureDir(ctx context.Context, root string, skip map[string]bool, goCache bool, cutoff time.Time) dirMeasure {
	return measureDirWithin(ctx, root, skip, goCache, cutoff, dirBudget, dirMaxEntries)
}

// measureDirWithin is measureDir with its own time and entry limits.
func measureDirWithin(ctx context.Context, root string, skip map[string]bool, goCache bool, cutoff time.Time, budget time.Duration, maxEntries int) dirMeasure {
	var m dirMeasure
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() {
		return m
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	rootDev, hasDev := deviceOf(rootInfo)
	entries := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil || entries >= maxEntries {
			m.approx = true
			return fs.SkipAll
		}
		if err != nil {
			if d != nil && d.IsDir() && p != root {
				return fs.SkipDir
			}
			return nil
		}
		entries++
		if d.IsDir() {
			if p == root {
				return nil
			}
			if skip[p] {
				return fs.SkipDir
			}
			if hasDev {
				if info, ierr := d.Info(); ierr == nil {
					if dev, ok := deviceOf(info); ok && dev != rootDev {
						return fs.SkipDir
					}
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		n := allocatedBytes(info)
		m.bytes += n
		if goCache && info.ModTime().Before(cutoff) {
			if shard := filepath.Dir(p); filepath.Dir(shard) == root && isShard(filepath.Base(shard)) {
				m.reclaimable += n
			}
		}
		return nil
	})
	return m
}

// overlayMount is one overlayfs mount and its upper (writable) directory.
type overlayMount struct {
	MountPoint string
	UpperDir   string
}

// procOverlayMounts lists overlay mounts from /proc/self/mounts. On a cloud
// machine /etc /usr /opt /var are overlays whose upper dirs persist apt
// installs on the docker volume, so their size is part of what fills it.
func procOverlayMounts() []overlayMount {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []overlayMount
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[2] != "overlay" {
			continue
		}
		for _, opt := range strings.Split(fields[3], ",") {
			if upper, ok := strings.CutPrefix(opt, "upperdir="); ok {
				out = append(out, overlayMount{MountPoint: unescapeMount(fields[1]), UpperDir: unescapeMount(upper)})
			}
		}
	}
	return out
}

// unescapeMount undoes /proc/mounts' octal escaping of spaces and tabs.
func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

// dockerAPI is the slice of the docker engine this package uses: its disk
// usage and a build cache prune. dockerengine.Service is the production one.
type dockerAPI interface {
	DiskUsage(ctx context.Context) (dockerengine.Usage, error)
	PruneBuildCache(ctx context.Context, olderThan time.Duration) (uint64, error)
}

// systemDocker is the engine on docker's local unix socket: DOCKER_HOST when
// it names one, else /var/run/docker.sock. Nil when DOCKER_HOST points
// elsewhere (a remote engine's disk is not this machine's).
func systemDocker(getenv func(string) string) dockerAPI {
	host := strings.TrimSpace(getenv("DOCKER_HOST"))
	switch {
	case host == "":
		return dockerengine.NewInstrumented(dockerengine.Deps{SocketPath: "/var/run/docker.sock"})
	case strings.HasPrefix(host, "unix://"):
		return dockerengine.NewInstrumented(dockerengine.Deps{SocketPath: strings.TrimPrefix(host, "unix://")})
	default:
		return nil
	}
}

// dockerConsumers reports docker's usage as one consumer per category. Only
// the build cache is clearable: pruning it loses nothing a later build cannot
// rebuild, while images, containers and volumes hold state a user may want.
func dockerConsumers(u dockerengine.Usage) []*reliantv1.DiskConsumer {
	mk := func(kind string, c dockerengine.Category) *reliantv1.DiskConsumer {
		return &reliantv1.DiskConsumer{
			Kind: kind, Label: ConsumerLabel(kind), Category: CategoryDocker,
			Bytes: c.Bytes, ReclaimableBytes: c.Reclaimable,
			Clearable: kind == KindDockerBuildCache,
		}
	}
	return []*reliantv1.DiskConsumer{
		mk(KindDockerImages, u.Images),
		mk(KindDockerContainers, u.Containers),
		mk(KindDockerVolumes, u.Volumes),
		mk(KindDockerBuildCache, u.BuildCache),
	}
}

// measureConsumers says what is using one volume: each cache directory on it,
// docker (when its data root is on it and dockerd is up), the overlay upper
// dirs persisting system packages, the worktree root, and the rest as
// "other". Biggest first, at most maxConsumers.
func (w *Watcher) measureConsumers(ctx context.Context, v *volume) []*reliantv1.DiskConsumer {
	cutoff := w.now().Add(-GoCacheTrimAge)
	var out []*reliantv1.DiskConsumer

	// Nested consumers (the default GOCACHE lives in ~/.cache) are measured
	// on their own and skipped by the walk of the one containing them.
	var measured []string
	for _, m := range v.members {
		if m.Kind != "" && m.resolved == m.Path {
			measured = append(measured, m.Path)
		}
	}
	// The worktree root goes first, with a budget of its own: it is usually
	// the biggest thing on the volume, and the cache walks below must not
	// spend the scan before it is reached.
	members := append([]member(nil), v.members...)
	sort.SliceStable(members, func(i, j int) bool { return members[i].Kind == KindWorktrees && members[j].Kind != KindWorktrees })
	for _, m := range members {
		if m.Kind == "" || m.resolved != m.Path {
			continue
		}
		skip := map[string]bool{}
		for _, other := range measured {
			if other != m.Path && strings.HasPrefix(other, m.Path+string(filepath.Separator)) {
				skip[other] = true
			}
		}
		var dm dirMeasure
		if m.Kind == KindWorktrees {
			// Tens of millions of files (node_modules, mostly): one walk
			// stopped after a few GB and "Other" got the rest. Each worktree
			// is measured on its own and remembered across scans.
			dm = w.wtSizes.measure(ctx, m.Path, w.now())
		} else {
			dm = measureDir(ctx, m.Path, skip, isGoFormatCache(m.Kind), cutoff)
		}
		if dm.bytes == 0 {
			continue
		}
		category := CategoryCaches
		if m.Kind == KindWorktrees {
			category = CategoryWorkspaces
		}
		out = append(out, &reliantv1.DiskConsumer{
			Kind: m.Kind, Label: m.Label, Category: category, Path: m.Path,
			Bytes: dm.bytes, Approximate: dm.approx, ReclaimableBytes: dm.reclaimable,
			Clearable: isGoFormatCache(m.Kind) && dm.reclaimable > 0,
		})
	}

	if v.has(RoleDocker) && w.docker != nil {
		if usage, err := w.docker.DiskUsage(ctx); err == nil {
			for _, c := range dockerConsumers(usage) {
				if c.Bytes > 0 {
					c.Clearable = c.Clearable && c.ReclaimableBytes > 0
					out = append(out, c)
				}
			}
		}
	}

	if v.has(RoleSystem) && w.mounts != nil {
		var sys dirMeasure
		var upperRoot string
		for _, om := range w.mounts() {
			for _, m := range v.members {
				if m.Role == RoleSystem && m.Path == om.MountPoint {
					dm := measureDir(ctx, om.UpperDir, nil, false, cutoff)
					sys.bytes += dm.bytes
					sys.approx = sys.approx || dm.approx
					if upperRoot == "" {
						upperRoot = filepath.Dir(om.UpperDir)
					}
				}
			}
		}
		if sys.bytes > 0 {
			out = append(out, &reliantv1.DiskConsumer{
				Kind: KindSystemPackages, Label: ConsumerLabel(KindSystemPackages), Category: CategorySystem,
				Path: upperRoot, Bytes: sys.bytes, Approximate: sys.approx,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	var named uint64
	for _, c := range out {
		named += c.Bytes
	}
	if u := v.usage(); u.UsedBytes > named && u.UsedBytes-named > u.TotalBytes/100 {
		out = append(out, &reliantv1.DiskConsumer{
			Kind: KindOther, Label: ConsumerLabel(KindOther), Category: CategoryOther, Bytes: u.UsedBytes - named,
		})
		sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	}
	if len(out) > maxConsumers {
		out = out[:maxConsumers]
	}
	return out
}
