// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

const (
	// ConsumerScanInterval is how often a volume under pressure is
	// re-measured. Measuring walks the cache directories (a 7 GB module cache
	// is ~300k entries), so it runs only while a volume is under pressure, at
	// once when pressure asserts, and then no more often than this.
	ConsumerScanInterval = 5 * time.Minute
	// consumerScanBudget bounds one whole scan; dirBudget and dirMaxEntries
	// bound one directory. A walk cut short reports a lower bound.
	consumerScanBudget = 60 * time.Second
	dirBudget          = 15 * time.Second
	dirMaxEntries      = 1_000_000
	// maxConsumers caps what a volume reports, biggest first.
	maxConsumers = 12
	// GoCacheTrimAge is the youngest Go-format cache entry a clean-up may
	// remove. Go bumps an entry's mtime on use once it is an hour stale, so
	// an entry untouched for two hours is not in use by a live build — the
	// floor forge's own storage GC uses, for the same reason.
	GoCacheTrimAge = 2 * time.Hour
	// dockerTimeout bounds one docker engine API call. /system/df sizes every
	// volume, which is the slow part.
	dockerTimeout = 20 * time.Second
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
	var m dirMeasure
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() {
		return m
	}
	ctx, cancel := context.WithTimeout(ctx, dirBudget)
	defer cancel()
	rootDev, hasDev := deviceOf(rootInfo)
	entries := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil || entries >= dirMaxEntries {
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

// dockerAPI reads docker's disk usage and prunes its build cache through the
// engine API. Talking to the socket directly, rather than through the docker
// CLI, matters on a cloud machine: the CLI there is a lazy-start shim that
// boots dockerd on first use, and a disk scan must never start a 3 GiB daemon.
// A socket nobody answers on is "docker is not running" — nothing to measure.
type dockerAPI interface {
	DiskUsage(ctx context.Context) ([]*reliantv1.DiskConsumer, error)
	PruneBuildCache(ctx context.Context, olderThan time.Duration) (uint64, error)
}

type dockerSocket struct{ path string }

// systemDocker is docker's local unix socket: DOCKER_HOST when it names one,
// else /var/run/docker.sock. Nil when DOCKER_HOST points elsewhere (a remote
// engine's disk is not this machine's).
func systemDocker(getenv func(string) string) dockerAPI {
	host := strings.TrimSpace(getenv("DOCKER_HOST"))
	switch {
	case host == "":
		return dockerSocket{path: "/var/run/docker.sock"}
	case strings.HasPrefix(host, "unix://"):
		return dockerSocket{path: strings.TrimPrefix(host, "unix://")}
	default:
		return nil
	}
}

func (s dockerSocket) client() *http.Client {
	return &http.Client{
		Timeout: dockerTimeout,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", s.path)
		}},
	}
}

// dockerDF is the part of GET /system/df this package reads.
type dockerDF struct {
	LayersSize int64 `json:"LayersSize"`
	Images     []struct {
		Containers int64 `json:"Containers"`
		Size       int64 `json:"Size"`
		SharedSize int64 `json:"SharedSize"`
	} `json:"Images"`
	Containers []struct {
		SizeRw int64  `json:"SizeRw"`
		State  string `json:"State"`
	} `json:"Containers"`
	Volumes []struct {
		UsageData *struct {
			Size     int64 `json:"Size"`
			RefCount int64 `json:"RefCount"`
		} `json:"UsageData"`
	} `json:"Volumes"`
	BuildCache []struct {
		Size   int64 `json:"Size"`
		InUse  bool  `json:"InUse"`
		Shared bool  `json:"Shared"`
	} `json:"BuildCache"`
}

func (s dockerSocket) DiskUsage(ctx context.Context) ([]*reliantv1.DiskConsumer, error) {
	if _, err := os.Stat(s.path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/system/df", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker /system/df: %s", resp.Status)
	}
	var df dockerDF
	if err := json.NewDecoder(resp.Body).Decode(&df); err != nil {
		return nil, err
	}
	return df.consumers(), nil
}

// consumers applies the docker CLI's own accounting (`docker system df`):
// images reclaimable are the layers no container uses; containers, the
// writable layers of stopped ones; volumes, those no container references;
// build cache, records neither in use nor shared with an image.
func (df dockerDF) consumers() []*reliantv1.DiskConsumer {
	pos := func(n int64) uint64 {
		if n < 0 {
			return 0
		}
		return uint64(n)
	}
	var usedByContainers int64
	for _, im := range df.Images {
		if im.Containers > 0 && im.Size >= 0 && im.SharedSize >= 0 {
			usedByContainers += im.Size - im.SharedSize
		}
	}
	var ctrSize, ctrReclaim int64
	for _, c := range df.Containers {
		ctrSize += c.SizeRw
		if c.State != "running" {
			ctrReclaim += c.SizeRw
		}
	}
	var volSize, volReclaim int64
	for _, v := range df.Volumes {
		if v.UsageData == nil || v.UsageData.Size < 0 {
			continue
		}
		volSize += v.UsageData.Size
		if v.UsageData.RefCount == 0 {
			volReclaim += v.UsageData.Size
		}
	}
	var bcSize, bcReclaim int64
	for _, b := range df.BuildCache {
		bcSize += b.Size
		if !b.InUse && !b.Shared {
			bcReclaim += b.Size
		}
	}
	mk := func(kind string, size, reclaim int64) *reliantv1.DiskConsumer {
		return &reliantv1.DiskConsumer{
			Kind: kind, Label: ConsumerLabel(kind), Category: CategoryDocker,
			Bytes: pos(size), ReclaimableBytes: pos(min(reclaim, size)),
			Clearable: kind == KindDockerBuildCache,
		}
	}
	return []*reliantv1.DiskConsumer{
		mk(KindDockerImages, df.LayersSize, df.LayersSize-usedByContainers),
		mk(KindDockerContainers, ctrSize, ctrReclaim),
		mk(KindDockerVolumes, volSize, volReclaim),
		mk(KindDockerBuildCache, bcSize, bcReclaim),
	}
}

func (s dockerSocket) PruneBuildCache(ctx context.Context, olderThan time.Duration) (uint64, error) {
	filters, _ := json.Marshal(map[string][]string{"until": {olderThan.String()}})
	u := "http://docker/build/prune?filters=" + url.QueryEscape(string(filters))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return 0, err
	}
	c := s.client()
	c.Timeout = 10 * time.Minute
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("docker /build/prune: %s", resp.Status)
	}
	var out struct {
		SpaceReclaimed int64 `json:"SpaceReclaimed"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.SpaceReclaimed < 0 {
		return 0, nil
	}
	return uint64(out.SpaceReclaimed), nil
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
	for _, m := range v.members {
		if m.Kind == "" || m.resolved != m.Path {
			continue
		}
		skip := map[string]bool{}
		for _, other := range measured {
			if other != m.Path && strings.HasPrefix(other, m.Path+string(filepath.Separator)) {
				skip[other] = true
			}
		}
		dm := measureDir(ctx, m.Path, skip, isGoFormatCache(m.Kind), cutoff)
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
		if docker, err := w.docker.DiskUsage(ctx); err == nil {
			for _, c := range docker {
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
