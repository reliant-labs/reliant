//go:build linux

package cli

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// portListenerPID returns the pid LISTENing on TCP <port> (IPv4 or IPv6, any
// local address), or 0 when none is found or the holder is not visible to
// this user.
//
// It reads the kernel's own tables rather than shelling out to lsof: lsof is
// absent from most minimal Linux images (Debian/Ubuntu slim, Alpine,
// devcontainers, sandboxed cloud workspaces), and on a machine without it
// every port holder came back unidentifiable. `forge env status` then
// reported no pid or ownership for a stack it had started itself, and the
// post-launch readiness gate degraded a stale foreign holder to "ours" — the
// exact trap it exists to catch. On Linux lsof reads these same /proc files,
// so this answers the same question with no dependency.
//
// A 0 return classifies the holder as unidentifiable → foreign, as before.
func portListenerPID(port int) int {
	return portListenerPIDIn("/proc", port)
}

// portListenerPIDIn is portListenerPID against an arbitrary proc root, so the
// table parsing and the fd scan are testable against a fixture tree.
func portListenerPIDIn(procRoot string, port int) int {
	if port <= 0 || port > 65535 {
		return 0
	}
	inodes := map[uint64]bool{}
	for _, table := range []string{"net/tcp", "net/tcp6"} {
		f, err := os.Open(filepath.Join(procRoot, table))
		if err != nil {
			continue // tcp6 is absent on a kernel without IPv6
		}
		for _, ino := range listenSocketInodes(f, port) {
			inodes[ino] = true
		}
		_ = f.Close() // read-only; a close error loses nothing
	}
	if len(inodes) == 0 {
		return 0
	}
	return socketHolderPID(procRoot, inodes)
}

// tcpStateListen is TCP_LISTEN in the kernel's st column (include/net/tcp_states.h).
const tcpStateListen = "0A"

// listenSocketInodes returns the socket inode of every LISTEN row on port in
// one /proc/net/tcp{,6} table. A row is
//
//	sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode ...
//
// with local_address as HEXADDR:HEXPORT. Only LISTEN rows count: an
// established connection that happens to share the port number (a client's
// ephemeral side, or an accepted connection) is not the port's holder.
func listenSocketInodes(r io.Reader, port int) []uint64 {
	var out []uint64
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 || f[3] != tcpStateListen {
			continue // the header row, a short line, or not listening
		}
		colon := strings.LastIndexByte(f[1], ':')
		if colon < 0 {
			continue
		}
		p, err := strconv.ParseUint(f[1][colon+1:], 16, 16)
		if err != nil || int(p) != port {
			continue
		}
		ino, err := strconv.ParseUint(f[9], 10, 64)
		if err != nil || ino == 0 {
			continue
		}
		out = append(out, ino)
	}
	return out
}

// socketHolderPID returns the lowest pid with an open fd on any of inodes, or
// 0. Lowest-first matches lsof's pid-sorted output (portListenerPID used to
// take its first line), so a socket shared across a fork resolves to the same
// process it always did. A pid whose fd directory is unreadable — another
// user's process, or one that exited mid-scan — is skipped, which is exactly
// what lsof does for a non-root caller.
func socketHolderPID(procRoot string, inodes map[uint64]bool) int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	for _, pid := range pids {
		fdDir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if ino, ok := socketLinkInode(link); ok && inodes[ino] {
				return pid
			}
		}
	}
	return 0
}

// socketLinkInode parses an fd symlink target of the form "socket:[<inode>]".
func socketLinkInode(link string) (uint64, bool) {
	rest, ok := strings.CutPrefix(link, "socket:[")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, "]")
	if !ok {
		return 0, false
	}
	ino, err := strconv.ParseUint(rest, 10, 64)
	return ino, err == nil
}
