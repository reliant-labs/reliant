// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

var (
	noSpaceRE = regexp.MustCompile(`(?i)no space left on device|\bENOSPC\b`)
	// absPathRE finds absolute paths in a line: what a failing write names
	// ("write /var/cache/workspace/go-build/39/…-d: no space left on device",
	// node's "ENOSPC: no space left on device, open '/tmp/x'").
	absPathRE = regexp.MustCompile(`(?:^|[\s'"(=\x60])(/[^\s:'"()\x60,]+)`)
)

// noSpaceMarker opens every explanation. Text that already carries one (an
// exec result relayed through a second error path) is not explained twice.
const noSpaceMarker = "[disk full]"

// MentionsNoSpace reports whether text carries an ENOSPC failure.
func MentionsNoSpace(text string) bool {
	return noSpaceRE.MatchString(text)
}

// noSpacePaths returns the absolute paths named on the lines that report
// ENOSPC, first five, in order.
func noSpacePaths(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if !noSpaceRE.MatchString(line) {
			continue
		}
		for _, m := range absPathRE.FindAllStringSubmatch(line, -1) {
			out = append(out, m[1])
			if len(out) == 5 {
				return out
			}
		}
	}
	return out
}

// ExplainNoSpace turns a failure's "no space left on device" into what the
// agent (and the person reading the tool result) needs: WHICH of the
// machine's volumes is full, how full, and what is using it — so a build is
// not retried blindly against a full disk.
//
// The volume is the one the failing path is on, when the text names a path
// that exists or is on a volume under pressure; otherwise the fullest volume
// under pressure. A fresh statfs is taken now. When no watched volume can be
// blamed — an ENOSPC from a container's own tmpfs, say — it returns "" rather
// than pin it on a disk that has room.
//
// It also records the failure on the volume and signals Changed, so the next
// heartbeat (sent at once) tells the UI that commands are failing.
func (w *Watcher) ExplainNoSpace(text string) string {
	if w == nil || !MentionsNoSpace(text) || strings.Contains(text, noSpaceMarker) {
		return ""
	}
	vols := groupVolumes(w.targetList(), w.stat)
	if len(vols) == 0 {
		return ""
	}

	w.mu.Lock()
	w.applyLocked(vols)
	var hit *volume
	for _, p := range noSpacePaths(text) {
		st, resolved, err := w.stat(p)
		if err != nil {
			continue
		}
		// The file a failed write names usually does not exist (the write
		// is what failed), so its parent directory is trusted too. A path
		// missing more than that is likely not on this machine at all (one
		// inside a container), and resolves to some far ancestor on any
		// volume: trust that only when the volume is actually under pressure.
		near := resolved == p || resolved == filepath.Dir(p)
		for _, v := range vols {
			if v.matches(st) && (near || w.pressure[v.key]) {
				hit = v
				break
			}
		}
		if hit != nil {
			break
		}
	}
	if hit == nil {
		for _, v := range vols {
			if !w.pressure[v.key] {
				continue
			}
			if hit == nil || v.usage().UsedFraction() > hit.usage().UsedFraction() {
				hit = v
			}
		}
	}
	if hit == nil {
		w.mu.Unlock()
		return ""
	}
	w.noSpaceAt[hit.key] = w.now().UnixMilli()
	dv := w.volumeProtoLocked(hit)
	if len(dv.Consumers) == 0 {
		// Not under pressure any more (something freed space), or not yet
		// measured: name the last breakdown anyway if there is one.
		dv.Consumers = w.scans[hit.key]
	}
	hasDocker := hit.has(RoleDocker)
	w.mu.Unlock()
	w.signal()
	return noSpaceMessage(dv, hasDocker)
}

// noSpaceMessage is the explanation appended to a failed tool result.
func noSpaceMessage(v *reliantv1.DiskVolume, hasDocker bool) string {
	var b strings.Builder
	if v.Pressure {
		fmt.Fprintf(&b, noSpaceMarker+" The machine's %s volume is out of space: %s free of %s",
			v.Label, FormatBytes(v.FreeBytes), FormatBytes(v.TotalBytes))
	} else {
		// The failing path is on this volume but it has room again (the
		// failed build deleted its partial output, say): say so rather than
		// call a volume with gigabytes free "out of space".
		fmt.Fprintf(&b, noSpaceMarker+" The machine's %s volume ran out of space; it has %s free of %s now, so something freed space since",
			v.Label, FormatBytes(v.FreeBytes), FormatBytes(v.TotalBytes))
	}
	u := Usage{FilesTotal: v.FilesTotal, FilesFree: v.FilesFree}
	if u.FilesTotal > 0 && u.InodeFraction() >= InodeClearFraction {
		fmt.Fprintf(&b, ", and %d%% of its inodes are used (too many small files)", int(u.InodeFraction()*100))
	}
	if len(v.Paths) > 0 {
		paths := v.Paths
		more := ""
		if len(paths) > 3 {
			paths, more = paths[:3], ", …"
		}
		fmt.Fprintf(&b, " (holds %s%s)", strings.Join(paths, ", "), more)
	}
	b.WriteString(".")
	if len(v.Consumers) > 0 {
		var parts []string
		for _, c := range v.Consumers {
			if len(parts) == 4 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s %s", c.Label, FormatBytes(c.Bytes)))
		}
		fmt.Fprintf(&b, " Largest: %s.", strings.Join(parts, ", "))
	}
	if v.Pressure {
		b.WriteString(" Retrying will fail the same way until space is freed.")
	} else {
		b.WriteString(" A large build can fill it again; free space before retrying one.")
	}
	if hasDocker {
		b.WriteString(" Safe to free without asking: docker's build cache (`docker builder prune -f --filter until=2h`).")
	}
	b.WriteString(" Do not prune docker images or volumes, or run `go clean -cache`/`go clean -modcache`, without the user's go-ahead: volumes can hold databases, and clearing the Go cache under running builds breaks them. The user can free rebuildable caches from this machine's storage item in the Reliant inbox. ")
	b.WriteString(NeverDelete)
	return b.String()
}

// NeverDelete closes every message that asks an agent to free space. Told to
// free space, an agent deleted another run's live state by glob
// (`rm -rf /tmp/cp-ci-affected.*`, mid-gate, 2026-10-22): temp directories
// look like litter and are often a running process's only copy of its work.
const NeverDelete = "Never glob-delete under /tmp (`rm -rf /tmp/<prefix>*`) or remove a directory another process or agent created: running builds, test gates and dev stacks keep their live state there, and deleting it breaks their work mid-run. Remove only files you created yourself."

// FormatBytes renders bytes in decimal units, one decimal under 10 ("1.4 GB"),
// matching the web's formatBytes so a number reads the same in a tool result
// and in the UI.
func FormatBytes(n uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	u := 0
	for v >= 1000 && u < len(units)-1 {
		v /= 1000
		u++
	}
	if u == 0 || v >= 10 {
		return fmt.Sprintf("%.0f %s", v, units[u])
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}
