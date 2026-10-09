// Copyright (c) 2025 Reliant Labs
package rootwatch

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrUnsupported is returned by the system Stat where the platform reports no
// device/inode pair (Windows). The watcher then records nothing and is inert.
var ErrUnsupported = errors.New("file identity is not available on this platform")

// mountInfoPath is the calling process's view of the mount table. It is per
// mount NAMESPACE, which is the point: a mount detached inside the container
// disappears from here even while the node still has it mounted.
const mountInfoPath = "/proc/self/mountinfo"

// SystemProbe looks at the real filesystem.
func SystemProbe() Probe {
	return Probe{Stat: statIdentity, MountPoint: systemMountPoint, Mounts: coveringMounts}
}

func withSystemDefaults(p Probe) Probe {
	sys := SystemProbe()
	if p.Stat == nil {
		p.Stat = sys.Stat
	}
	if p.MountPoint == nil {
		p.MountPoint = sys.MountPoint
	}
	if p.Mounts == nil {
		p.Mounts = sys.Mounts
	}
	return p
}

func statIdentity(path string) (Identity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Identity{}, err
	}
	id, ok := identityOf(info)
	if !ok {
		return Identity{}, ErrUnsupported
	}
	return id, nil
}

// systemMountPoint answers from the mount table where there is one (Linux):
// it is authoritative, and the device-number comparison is not — a btrfs
// subvolume has its own st_dev without being a mount point, and two volumes
// served over one virtiofs share can share one. Without a mount table (macOS)
// a directory on a different device than its parent is a mount point.
func systemMountPoint(path string) (isMount, known bool) {
	if data, err := os.ReadFile(mountInfoPath); err == nil {
		target := resolve(path)
		for _, m := range parseMountInfo(data) {
			if m.mountPoint == target {
				return true, true
			}
		}
		return false, true
	}
	self, err := statIdentity(path)
	if err != nil {
		return false, false
	}
	parent, err := statIdentity(filepath.Dir(path))
	if err != nil {
		return false, false
	}
	return self.Dev != parent.Dev, true
}

// coveringMounts returns the mount-table lines for path and each of its
// ancestors, outermost first — the stack of mounts that decides what path
// resolves to.
func coveringMounts(path string) []string {
	data, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return nil
	}
	target := resolve(path)
	var lines []string
	for _, m := range parseMountInfo(data) {
		if within(target, m.mountPoint) {
			lines = append(lines, m.line)
		}
	}
	return lines
}

type mountInfoEntry struct {
	mountPoint string
	line       string
}

// parseMountInfo reads proc(5) mountinfo: the fifth field of each line is the
// mount point, with space, tab, newline and backslash octal-escaped.
func parseMountInfo(data []byte) []mountInfoEntry {
	var entries []mountInfoEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		entries = append(entries, mountInfoEntry{mountPoint: unescapeOctal(fields[4]), line: line})
	}
	return entries
}

func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// resolve follows symlinks so a path compares equal to the mount point the
// kernel lists, falling back to the cleaned path when it cannot be resolved
// (which is itself the state being diagnosed).
func resolve(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}
