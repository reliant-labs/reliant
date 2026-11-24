// Copyright (c) 2025 Reliant Labs
//go:build linux

package shell

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/netports"
)

// procRoot is where the port scan reads procfs.
const procRoot = "/proc"

// childPIDSnapshot is the parent PID -> child PIDs map of the whole process
// table, read from procfs: no subprocess. It falls back to `ps` only when
// procfs cannot be read at all.
func childPIDSnapshot() map[int][]int {
	if children, ok := procChildPIDSnapshot(procRoot); ok {
		return children
	}
	return psChildPIDSnapshot()
}

// getPortsForPids is the TCP LISTEN ports of pids, keyed by owning PID, read
// from procfs: each PID's socket fds, matched against its network namespace's
// /proc/<pid>/net/tcp{,6} by socket inode. It falls back to `lsof` only when
// procfs has no TCP tables.
//
// This is the cost of exec.bg_list (the web's process list, polled). One
// `lsof` costs about a second of fixed startup on a busy Linux machine, however
// few PIDs it is asked about (0.8s for 5 PIDs and 1.0s for 828 at load 84,
// measured 2026-11-21), and prod's exec.bg_list ran 2.1s p50 and timed out at
// 5s on one in five calls. Reading the same facts from procfs took 37ms for
// every process on that machine.
func getPortsForPids(pids []int) (map[int][]PortInfo, error) {
	if ports, ok := procPortsForPids(procRoot, pids); ok {
		return ports, nil
	}
	return lsofPortsForPids(pids)
}

// procChildPIDSnapshot builds the parent -> children map from each
// <root>/<pid>/stat. ok is false when root lists no process at all.
func procChildPIDSnapshot(root string) (map[int][]int, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, false
	}
	children := make(map[int][]int)
	found := false
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		ppid, ok := procParentPID(filepath.Join(root, entry.Name(), "stat"))
		if !ok {
			continue // exited since the directory was read
		}
		found = true
		children[ppid] = append(children[ppid], pid)
	}
	return children, found
}

// procParentPID reads the ppid from a /proc/<pid>/stat line:
// "pid (comm) state ppid ...". comm may hold spaces and parentheses, so the
// fields are counted from the LAST ')'.
func procParentPID(statPath string) (int, bool) {
	raw, err := os.ReadFile(statPath)
	if err != nil {
		return 0, false
	}
	stat := string(raw)
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// procPortsForPids attributes the TCP LISTEN sockets of pids to the PIDs whose
// fds hold them. ok is false when root has no TCP table to read (procfs is not
// mounted), so the caller can fall back to lsof. A PID that has exited, or
// whose fds are not ours to read, simply contributes nothing, as with lsof.
func procPortsForPids(root string, pids []int) (map[int][]PortInfo, bool) {
	if _, err := os.Stat(filepath.Join(root, "net", "tcp")); err != nil {
		return nil, false
	}
	ports := make(map[int][]PortInfo, len(pids))
	if len(pids) == 0 {
		return ports, true
	}

	// Socket inode -> the requested PIDs holding it (a forked child shares its
	// parent's listening socket, and lsof lists it under each).
	owners := make(map[uint64][]int)
	// One PID per network namespace: the TCP tables are per namespace, so each
	// is read once however many PIDs share it.
	netnsPid := make(map[string]int)
	var netnsOrder []string
	for _, pid := range pids {
		pidDir := filepath.Join(root, strconv.Itoa(pid))
		inodes := procSocketInodes(filepath.Join(pidDir, "fd"))
		if len(inodes) == 0 {
			continue
		}
		for _, inode := range inodes {
			owners[inode] = append(owners[inode], pid)
		}
		ns, err := os.Readlink(filepath.Join(pidDir, "ns", "net"))
		if err != nil {
			ns = "pid:" + strconv.Itoa(pid) // unknown namespace: read this PID's own tables
		}
		if _, seen := netnsPid[ns]; !seen {
			netnsPid[ns] = pid
			netnsOrder = append(netnsOrder, ns)
		}
	}
	if len(owners) == 0 {
		return ports, true
	}

	for _, ns := range netnsOrder {
		netDir := filepath.Join(root, strconv.Itoa(netnsPid[ns]), "net")
		for _, table := range []string{"tcp", "tcp6"} {
			f, err := os.Open(filepath.Join(netDir, table))
			if err != nil {
				continue
			}
			listeners := netports.ParseListeners(f)
			_ = f.Close()
			for _, l := range listeners {
				for _, pid := range owners[l.Inode] {
					ports[pid] = append(ports[pid], PortInfo{
						Port:     l.Port,
						Protocol: "tcp",
						State:    "LISTEN",
						Address:  listenAddress(l),
					})
				}
			}
		}
	}

	for pid := range ports {
		sort.SliceStable(ports[pid], func(i, j int) bool { return ports[pid][i].Port < ports[pid][j].Port })
	}
	return ports, true
}

// procSocketInodes is the socket inodes a /proc/<pid>/fd directory holds
// ("socket:[12345]" links).
func procSocketInodes(fdDir string) []uint64 {
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil
	}
	var inodes []uint64
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue
		}
		rest, ok := strings.CutPrefix(target, "socket:[")
		if !ok {
			continue
		}
		inode, err := strconv.ParseUint(strings.TrimSuffix(rest, "]"), 10, 64)
		if err != nil {
			continue
		}
		inodes = append(inodes, inode)
	}
	return inodes
}

// listenAddress renders a listener's bind address the way the lsof parser
// does: a wildcard bind is "0.0.0.0" (lsof's "*"), an IPv6 address is
// bracketed ("[::1]").
func listenAddress(l netports.Listener) string {
	if l.IP.IsUnspecified() {
		return "0.0.0.0"
	}
	if v4 := l.IP.To4(); v4 != nil {
		return v4.String()
	}
	return "[" + l.IP.String() + "]"
}
