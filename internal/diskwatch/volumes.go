// Copyright (c) 2025 Reliant Labs
package diskwatch

// fsStat is one path's filesystem reading.
type fsStat struct {
	// Dev is the st_dev of the path itself.
	Dev    uint64
	Bsize  uint64
	Blocks uint64
	Bfree  uint64
	Bavail uint64
	Files  uint64
	Ffree  uint64
}

func (s fsStat) usage() Usage {
	used := uint64(0)
	if s.Blocks > s.Bfree {
		used = (s.Blocks - s.Bfree) * s.Bsize
	}
	return Usage{
		TotalBytes: s.Blocks * s.Bsize,
		UsedBytes:  used,
		FreeBytes:  s.Bavail * s.Bsize,
		FilesTotal: s.Files,
		FilesFree:  s.Ffree,
	}
}

// sameFilesystem reports whether two readings with different st_dev are the
// same filesystem seen through two mounts.
//
// st_dev alone is not enough, as verified on a cloud machine: the overlays
// over /etc /usr /opt /var each have their own st_dev (and fsid), yet statfs
// through them reports the upper dir's filesystem — /dev/docker-data — block
// for block; and / and /tmp are two fuse mounts (two st_devs) of one 96 GiB
// filesystem. Equal geometry (block size, block count, inode count) with free
// blocks within 0.1% of the volume is the same filesystem: the readings are
// taken back to back, so a write landing between them must not split the
// volume in two. A false merge needs two distinct filesystems of identical
// geometry used to within 0.1% of each other — and even then joins two labels
// without hiding any pressure.
func sameFilesystem(a, b fsStat) bool {
	if a.Blocks == 0 || a.Bsize != b.Bsize || a.Blocks != b.Blocks || a.Files != b.Files {
		return false
	}
	diff := a.Bfree - b.Bfree
	if b.Bfree > a.Bfree {
		diff = b.Bfree - a.Bfree
	}
	return diff <= a.Blocks/1000
}

// member is one target resolved to a filesystem.
type member struct {
	Target
	resolved string
}

// volume is the targets that resolved to one filesystem.
type volume struct {
	// key identifies the volume across samples (for its pressure bit): the
	// st_dev of its first target, in DiscoverTargets' fixed order.
	key     uint64
	devs    []uint64
	stat    fsStat
	members []member
}

func (v *volume) usage() Usage { return v.stat.usage() }

func (v *volume) has(r Role) bool {
	for _, m := range v.members {
		if m.Role == r {
			return true
		}
	}
	return false
}

func (v *volume) hasDev(dev uint64) bool {
	for _, d := range v.devs {
		if d == dev {
			return true
		}
	}
	return false
}

// matches reports whether a reading of some other path is this volume.
func (v *volume) matches(st fsStat) bool {
	return v.hasDev(st.Dev) || sameFilesystem(v.stat, st)
}

// paths lists the volume's watched paths in target order.
func (v *volume) paths() []string {
	out := make([]string, 0, len(v.members))
	for _, m := range v.members {
		out = append(out, m.Path)
	}
	return out
}

// label names what the volume holds. Home wins: on a laptop the caches live in
// the home directory, and "home" is what the user calls that disk. Otherwise
// caches and docker — the cloud machine's shared volume — then system, then
// temp.
func (v *volume) label() string {
	caches, docker := v.has(RoleCache), v.has(RoleDocker)
	switch {
	case v.has(RoleHome) || v.has(RoleWorktrees):
		return "home"
	case caches && docker:
		return "build caches & docker"
	case caches:
		return "build caches"
	case docker:
		return "docker"
	case v.has(RoleSystem):
		return "system"
	case v.has(RoleTemp):
		return "temp"
	default:
		return "disk"
	}
}

// groupVolumes resolves each target and groups them by filesystem.
func groupVolumes(targets []Target, stat func(string) (fsStat, string, error)) []*volume {
	var vols []*volume
	for _, t := range targets {
		st, resolved, err := stat(t.Path)
		if err != nil || st.Blocks == 0 {
			continue
		}
		if t.MustExist && resolved != t.Path {
			continue
		}
		var v *volume
		for _, cand := range vols {
			if cand.matches(st) {
				v = cand
				break
			}
		}
		if v == nil {
			v = &volume{key: st.Dev, stat: st}
			vols = append(vols, v)
		}
		if !v.hasDev(st.Dev) {
			v.devs = append(v.devs, st.Dev)
		}
		v.members = append(v.members, member{Target: t, resolved: resolved})
	}
	return vols
}
