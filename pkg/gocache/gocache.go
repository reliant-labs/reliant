// Package gocache is the one policy every Go build cache on a machine is
// trimmed by: forge's storage maintenance (`forge machine storage gc` and
// `forge machine storage gocache`) and the Reliant daemon's storage reaper both
// read it here, so the two can never drift apart.
//
// The trim itself is forge's (an LRU over the cache's 256 shards, safe beside
// running builds); this package only says how far it may go:
//
//   - The age rule: an entry unused for DefaultUnused is removed.
//   - The size rule: a cache over its budget (DefaultBudgetGiB, capped at
//     DefaultVolumePercent of the volume it lives on) is trimmed least recently
//     used first down to LowWater of that budget.
//   - The floor: neither rule removes an entry used within Floor, and the size
//     rule reaches toward MinFloor only when everything older cannot meet the
//     budget. Nothing ever removes an entry used within MinFloor.
//
// Why the floor is what it is. cmd/go restamps a cache entry's mtime on use
// only once it is more than an hour stale (cmd/go/internal/cache's
// mtimeInterval), so an mtime younger than an hour says nothing about whether
// a running build is about to open that entry. Removing one a build has
// resolved but not yet read fails it with "could not load export data … no
// such file or directory". Floor is that hour plus an hour of margin; MinFloor
// is that hour plus five minutes, the most the size rule may take to stop a
// disk filling. Neither is a knob.
//
// Every Go-format cache gets its own budget from the same policy: the shared
// GOCACHE, the golangci-lint cache (DefaultGolangciBudgetGiB), and any other
// cache a machine declares or a daemon discovers.
package gocache

import (
	"io"
	"os"
	"path/filepath"
	"time"
)

// GiB is one gibibyte in bytes.
const GiB = uint64(1 << 30)

const (
	// Floor is the youngest last use either rule removes while older entries
	// can still bring a cache under its budget: cmd/go's one-hour restamp
	// interval plus an hour of margin.
	Floor = 2 * time.Hour

	// MinFloor is the youngest last use any trim may ever remove, under any
	// budget pressure: cmd/go's one-hour restamp interval plus five minutes.
	MinFloor = time.Hour + 5*time.Minute

	// DefaultUnused is the age rule: an entry unused this long is removed
	// whatever the cache's size. A day, so a machine that builds many
	// branches does not keep days of entries nothing will read again; a
	// rebuild of one is a compile, not a download.
	DefaultUnused = 24 * time.Hour

	// DefaultBudgetGiB is each Go build cache's size budget before the
	// volume cap (DefaultVolumePercent).
	DefaultBudgetGiB = uint64(60)

	// DefaultGolangciBudgetGiB is the golangci-lint cache's budget. Its own,
	// because its entries are tiny and recomputed in seconds: under the build
	// cache's budget it was never trimmed.
	DefaultGolangciBudgetGiB = uint64(4)

	// DefaultVolumePercent caps a cache's budget at this share of the volume
	// it lives on, so one default fits a 40 GiB build volume and a 1 TB
	// laptop alike.
	DefaultVolumePercent = 15

	// LowWater is the share of the budget an over-budget trim brings a cache
	// down to. The slack keeps the next pass from trimming a handful of
	// entries every time.
	LowWater = 0.8
)

// Policy is the part of the trim policy a machine may tune. The zero value of
// a field means its default.
type Policy struct {
	// BudgetGiB is the cache's size budget before the volume cap.
	BudgetGiB uint64
	// Unused is the age rule. A value inside Floor is raised to it.
	Unused time.Duration
	// VolumePercent caps the budget at this share of the cache's volume;
	// 100 or more leaves BudgetGiB alone.
	VolumePercent int
}

// Default is the policy for a Go build cache.
func Default() Policy {
	return Policy{BudgetGiB: DefaultBudgetGiB, Unused: DefaultUnused, VolumePercent: DefaultVolumePercent}
}

// Budget is the size in bytes the cache is held to on a volume of
// volumeCapacity bytes: BudgetGiB, capped at VolumePercent of the volume. A
// capacity of zero (unknown) leaves BudgetGiB alone.
func (p Policy) Budget(volumeCapacity uint64) uint64 {
	gib := p.BudgetGiB
	if gib == 0 {
		gib = DefaultBudgetGiB
	}
	budget := gib * GiB
	pct := p.VolumePercent
	if pct == 0 {
		pct = DefaultVolumePercent
	}
	if pct >= 100 || volumeCapacity == 0 {
		return budget
	}
	if v := volumeCapacity / 100 * uint64(pct); v < budget {
		return v
	}
	return budget
}

// Target is the size an over-budget trim brings the cache down to.
func (p Policy) Target(volumeCapacity uint64) uint64 {
	return uint64(float64(p.Budget(volumeCapacity)) * LowWater)
}

// AgeCutoff is the last-use time the age rule removes entries before, at now:
// Unused ago, but never inside Floor.
func (p Policy) AgeCutoff(now time.Time) time.Time {
	unused := p.Unused
	if unused == 0 {
		unused = DefaultUnused
	}
	if unused < Floor {
		unused = Floor
	}
	return now.Add(-unused)
}

// readmePrefix is the first line shared by the README the Go toolchain writes
// into every GOCACHE root and golangci-lint's fork of it.
const readmePrefix = "This directory holds cached build artifacts from"

// IsCache reports whether dir is the root of a Go-format build cache, by the
// README the toolchain writes there. A cache's location is configurable, so
// its name proves nothing: only this marker makes a directory safe to trim.
func IsCache(dir string) bool {
	f, err := os.Open(filepath.Join(dir, "README"))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, len(readmePrefix))
	n, _ := io.ReadFull(f, buf)
	return string(buf[:n]) == readmePrefix
}
