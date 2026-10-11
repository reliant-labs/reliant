// Copyright (c) 2025 Reliant Labs

// Package diskwatch watches the filesystems the work on a machine writes to,
// says what is filling the one that is full, and explains a "no space left on
// device" in those terms.
//
// Why a package and not one statfs of $HOME: on a cloud machine the volume
// that fills is not the home volume. The Go build and module caches, npm,
// XDG, golangci-lint, docker's images, volumes and build cache, AND the
// overlays that persist apt installs under /etc /usr /opt /var all share one
// small block volume (/dev/docker-data, 40 GiB on the machine that motivated
// this), while /home/workspace sits at 21%. A disk signal that measures the
// worktree root reports "fine" while every build fails with ENOSPC.
//
// So the daemon discovers the paths that matter (targets.go), groups them by
// the filesystem they resolve to (volumes.go), keeps a hysteresis pressure bit
// per volume (this file), measures what is using a volume only while it is
// under pressure (consumers.go), and reports the lot on its heartbeat as a
// reliantv1.DiskReport (watcher.go). The server stores that report and the UI,
// the storage inbox item and the agent's tool results all read it.
//
//forge:exclude-contract: statfs and directory walks over the daemon's own filesystems; one implementation, nothing to substitute
package diskwatch

// GiB is 2^30 bytes.
const GiB uint64 = 1 << 30

// Pressure thresholds. The reasoning is in docs: the numbers are tuned for
// the failure they warn about — the next build burst not fitting — rather
// than for a percentage that looks familiar.
//
//   - PressureFreeFloor (2 GiB): one ordinary write burst — a `go build` of a
//     large module, a docker layer extraction, an `npm ci`. Below it the next
//     build can fail. It only matters below 20 GiB (above that 10% is already
//     more), i.e. on exactly the 5–10 GiB docker volumes that are most at
//     risk. A volume smaller than 8 GiB scales it to a quarter of itself, so a
//     small tmpfs is not permanently "full".
//   - PressureUsedFraction (90%) with PressureFreeCeiling (20 GiB): bigger
//     volumes host bigger workloads, so they warn proportionally earlier; the
//     ceiling keeps a 1 TB laptop at 91% (90 GB free) from raising a
//     permanent alert — no build burst exhausts 20 GiB between two samples.
//   - The clear thresholds sit a step below (85%, 1.5× the floor, 24 GiB),
//     mirroring cgroupmem's 85/75 hysteresis: a cache filling and trimming
//     around one line must not flap the banner.
//   - Inodes: ENOSPC is also what a full inode table returns, and module
//     caches are small-file heavy. Without this a "disk full" error next to
//     "5 GB free" reads as a lie.
const (
	PressureFreeFloor         = 2 * GiB
	PressureUsedFraction      = 0.90
	PressureFreeCeiling       = 20 * GiB
	PressureClearUsedFraction = 0.85
	PressureClearFreeCeiling  = 24 * GiB
	InodePressureFraction     = 0.95
	InodeClearFraction        = 0.90
)

// Usage is one statfs reading of a volume, in bytes and inodes.
type Usage struct {
	// TotalBytes is the filesystem size (f_blocks).
	TotalBytes uint64
	// UsedBytes is f_blocks - f_bfree: what df reports as Used.
	UsedBytes uint64
	// FreeBytes is what an unprivileged process can still write (f_bavail).
	FreeBytes uint64
	// FilesTotal and FilesFree are inode counts; 0 when not reported.
	FilesTotal uint64
	FilesFree  uint64
}

// UsedFraction is df's Use%: used / (used + available), so blocks reserved for
// root count as full, which is what an unprivileged build experiences.
func (u Usage) UsedFraction() float64 {
	den := u.UsedBytes + u.FreeBytes
	if den == 0 {
		return 0
	}
	return float64(u.UsedBytes) / float64(den)
}

// InodeFraction is the fraction of inodes in use; 0 when not reported.
func (u Usage) InodeFraction() float64 {
	if u.FilesTotal == 0 || u.FilesFree > u.FilesTotal {
		return 0
	}
	return float64(u.FilesTotal-u.FilesFree) / float64(u.FilesTotal)
}

// freeFloor is PressureFreeFloor, scaled down to a quarter of a volume
// smaller than 8 GiB.
func freeFloor(total uint64) uint64 {
	return min(PressureFreeFloor, total/4)
}

// NextPressure is the pure hysteresis transition for one volume: assert at
// free < floor, or used >= 90% with free < 20 GiB, or inodes >= 95%; once
// asserted, hold until free >= 1.5×floor and (used < 85% or free >= 24 GiB)
// and inodes < 90%. A volume with no size is never under pressure.
func NextPressure(current bool, u Usage) bool {
	if u.TotalBytes == 0 {
		return false
	}
	floor := freeFloor(u.TotalBytes)
	if current {
		return u.FreeBytes < floor*3/2 ||
			(u.UsedFraction() >= PressureClearUsedFraction && u.FreeBytes < PressureClearFreeCeiling) ||
			(u.FilesTotal > 0 && u.InodeFraction() >= InodeClearFraction)
	}
	return u.FreeBytes < floor ||
		(u.UsedFraction() >= PressureUsedFraction && u.FreeBytes < PressureFreeCeiling) ||
		(u.FilesTotal > 0 && u.InodeFraction() >= InodePressureFraction)
}
