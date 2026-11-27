// Copyright (c) 2026 Reliant Labs

package diskwatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/reliant-labs/forge/pkg/gocache"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Every Go build cache on the machine, not only the shared one.
//
// forge's Go-cache trim (`forge machine storage gocache`, and the Go-cache layer
// of `auto-gc`) holds the shared `go env GOCACHE` and every cache the machine's
// storage policy declares (go_caches) to forge/pkg/gocache's policy. A build
// that points GOCACHE somewhere else — a worktree's own cache, a tool that sets
// it per run — writes a cache neither of those names, and nothing trimmed it.
//
// So the daemon remembers every Go build cache it sees a build use: on each
// reaper poll it reads the GOCACHE of the Go toolchain processes running on
// the machine (gocaches_linux.go: this user's, in this mount namespace, so the
// path is one the daemon can reach), and records it in goCachesSeenName beside
// the storage policy. Both forge passes are handed each remembered cache with
// `--go-cache`, and forge trims it like the shared one: the same LRU, safe
// beside running builds, to its own go_cache_gib budget. A cache unseen for
// goCacheForgetAfter is forgotten: by then the age rule has emptied it.
//
// A cache another user's processes build into (a CI runner's) is not readable
// here, so it is declared instead, in the policy's go_caches.

const (
	// goCachesSeenName is the record of the Go build caches the daemon saw
	// builds use, beside the storage policy.
	goCachesSeenName = "reliant-go-caches.json"
	// goCacheForgetAfter is how long a cache stays remembered after a build
	// was last seen using it: several times the age rule, which removes every
	// entry unused for longer.
	goCacheForgetAfter = 7 * 24 * time.Hour
	// goCacheRestampAfter is how stale a remembered sighting may grow before
	// a new one is written, so the record is not rewritten every poll.
	goCacheRestampAfter = time.Hour
	// maxGoCachesSeen bounds the record: the most recently seen are kept.
	maxGoCachesSeen = 64
)

// goCacheSighting is one remembered cache.
type goCacheSighting struct {
	Root     string    `json:"root"`
	LastSeen time.Time `json:"last_seen"`
}

type goCachesSeen struct {
	Caches []goCacheSighting `json:"caches"`
}

// goCachesSeenPath is the record's location for the policy at policyPath.
func goCachesSeenPath(policyPath string) string {
	if policyPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(policyPath), goCachesSeenName)
}

// readGoCachesSeen reads the record; a missing or unreadable one is empty.
func readGoCachesSeen(policyPath string) goCachesSeen {
	var s goCachesSeen
	path := goCachesSeenPath(policyPath)
	if path == "" {
		return s
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// rememberedGoCaches is every cache the record holds that is still a Go build
// cache, the roots handed to forge with --go-cache.
func rememberedGoCaches(policyPath string) []string {
	var out []string
	for _, c := range readGoCachesSeen(policyPath).Caches {
		if filepath.IsAbs(c.Root) && gocache.IsCache(c.Root) {
			out = append(out, c.Root)
		}
	}
	return out
}

// observeGoCaches records the caches the running builds use. It never fails a
// tick: an unwritable record is a WARN, and the caches stay untrimmed until it
// can be written.
func (w *Watcher) observeGoCaches(ctx context.Context, policyPath string) {
	scan := w.reaper.scanGoCaches
	if scan == nil || policyPath == "" {
		return
	}
	next, changed := mergeGoCacheSightings(readGoCachesSeen(policyPath), scan(), w.now())
	if !changed {
		return
	}
	path := goCachesSeenPath(policyPath)
	if err := writeGoCachesSeen(path, next); err != nil {
		logging.WarnContext(ctx, "[diskwatch] Could not record the Go build caches builds use; they are not trimmed until it can be", "path", path, "error", err)
	}
}

// mergeGoCacheSightings folds the roots seen now into the record: a new root
// is added, a stale sighting restamped, and a cache unseen for
// goCacheForgetAfter, or no longer a Go build cache, dropped. changed is
// whether the record must be rewritten.
func mergeGoCacheSightings(prev goCachesSeen, seen []string, now time.Time) (goCachesSeen, bool) {
	byRoot := map[string]time.Time{}
	changed := false
	for _, c := range prev.Caches {
		if now.Sub(c.LastSeen) > goCacheForgetAfter || !gocache.IsCache(c.Root) {
			changed = true
			continue
		}
		byRoot[c.Root] = c.LastSeen
	}
	for _, root := range seen {
		root = filepath.Clean(root)
		if !filepath.IsAbs(root) || !gocache.IsCache(root) {
			continue
		}
		if last, ok := byRoot[root]; !ok || now.Sub(last) >= goCacheRestampAfter {
			byRoot[root] = now
			changed = true
		}
	}
	var next goCachesSeen
	for root, last := range byRoot {
		next.Caches = append(next.Caches, goCacheSighting{Root: root, LastSeen: last})
	}
	sort.Slice(next.Caches, func(i, j int) bool {
		if !next.Caches[i].LastSeen.Equal(next.Caches[j].LastSeen) {
			return next.Caches[i].LastSeen.After(next.Caches[j].LastSeen)
		}
		return next.Caches[i].Root < next.Caches[j].Root
	})
	if len(next.Caches) > maxGoCachesSeen {
		next.Caches, changed = next.Caches[:maxGoCachesSeen], true
	}
	return next, changed
}

func writeGoCachesSeen(path string, s goCachesSeen) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// goCacheFlags is --go-cache for each cache the record holds.
func goCacheFlags(policyPath string) []string {
	var args []string
	for _, root := range rememberedGoCaches(policyPath) {
		args = append(args, "--go-cache", root)
	}
	return args
}

// goCacheRootFromEnv is the GOCACHE a process with environment env builds
// into: GOCACHE itself, else the toolchain's default under XDG_CACHE_HOME or
// HOME (os.UserCacheDir on Linux). "" when it has none ("off", or no home).
func goCacheRootFromEnv(env map[string]string) string {
	switch v := env["GOCACHE"]; {
	case v == "off":
		return ""
	case v != "":
		return v
	}
	if x := env["XDG_CACHE_HOME"]; filepath.IsAbs(x) {
		return filepath.Join(x, "go-build")
	}
	if h := env["HOME"]; filepath.IsAbs(h) {
		return filepath.Join(h, ".cache", "go-build")
	}
	return ""
}
