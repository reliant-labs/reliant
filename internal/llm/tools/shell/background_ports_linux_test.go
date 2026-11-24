// Copyright (c) 2025 Reliant Labs
//go:build linux

package shell

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// wantPortScanForks is how many `ps` and `lsof` processes one port scan
// starts: none on Linux, where procfs answers.
const wantPortScanForks = 0

// The port scan reads a listening socket this process holds straight from
// procfs, with no subprocess.
func TestProcPortsForPids_FindsThisProcessesListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer ln.Close()
	want := ln.Addr().(*net.TCPAddr).Port

	ports, ok := procPortsForPids(procRoot, []int{os.Getpid()})
	if !ok {
		t.Skip("procfs has no TCP tables on this host")
	}
	for _, p := range ports[os.Getpid()] {
		if p.Port == want {
			if p.Address != "127.0.0.1" || p.Protocol != "tcp" || p.State != "LISTEN" {
				t.Errorf("port %d = %+v, want 127.0.0.1 tcp LISTEN", want, p)
			}
			return
		}
	}
	t.Errorf("listening port %d not reported for pid %d: %+v", want, os.Getpid(), ports)
}

// A fake procfs pins the attribution: a socket is reported for every requested
// PID holding it, only LISTEN rows count, each namespace's tables are read
// from a PID in it, and unrequested PIDs are never reported.
func TestProcPortsForPids_AttributesBySocketInode(t *testing.T) {
	root := t.TempDir()
	writeFile := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, rel string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	const header = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	tcp := header +
		// 127.0.0.1:3000 LISTEN, inode 111
		"   0: 0100007F:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0 100 0 0 10 0\n" +
		// 0.0.0.0:5173 LISTEN, inode 222
		"   1: 00000000:1435 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 222 1 0 100 0 0 10 0\n" +
		// an ESTABLISHED socket, inode 333: never a port
		"   2: 0100007F:9C40 0100007F:0BB8 01 00000000:00000000 00:00000000 00000000  1000        0 333 1 0 100 0 0 10 0\n" +
		// a listener nobody requested holds, inode 444
		"   3: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 444 1 0 100 0 0 10 0\n"
	tcp6 := header +
		// [::1]:8080 LISTEN, inode 555
		"   0: 00000000000000000000000001000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 555 1 0 100 0 0 10 0\n"
	writeFile("net/tcp", tcp)
	for _, pid := range []string{"10", "11"} {
		writeFile(pid+"/net/tcp", tcp)
		writeFile(pid+"/net/tcp6", tcp6)
		link("net:[4026531840]", pid+"/ns/net")
	}
	// 10 holds 3000 and the established socket; its child 11 inherited 3000
	// and opened 5173 and [::1]:8080. 12 has no fds dir (exited).
	link("socket:[111]", "10/fd/3")
	link("socket:[333]", "10/fd/4")
	link("/dev/null", "10/fd/0")
	link("socket:[111]", "11/fd/3")
	link("socket:[222]", "11/fd/5")
	link("socket:[555]", "11/fd/6")

	ports, ok := procPortsForPids(root, []int{10, 11, 12})
	if !ok {
		t.Fatal("procPortsForPids reported no procfs")
	}
	want := map[int][]PortInfo{
		10: {{Port: 3000, Protocol: "tcp", State: "LISTEN", Address: "127.0.0.1"}},
		11: {
			{Port: 3000, Protocol: "tcp", State: "LISTEN", Address: "127.0.0.1"},
			{Port: 5173, Protocol: "tcp", State: "LISTEN", Address: "0.0.0.0"},
			{Port: 8080, Protocol: "tcp", State: "LISTEN", Address: "[::1]"},
		},
	}
	if !reflect.DeepEqual(ports, want) {
		t.Errorf("ports = %+v\nwant    %+v", ports, want)
	}

	if _, ok := procPortsForPids(t.TempDir(), []int{10}); ok {
		t.Error("a root with no net/tcp must report !ok so the caller falls back to lsof")
	}
}

// The process-table snapshot reads ppid after the LAST ')', since a command
// name may itself hold spaces and parentheses.
func TestProcChildPIDSnapshot(t *testing.T) {
	root := t.TempDir()
	stats := map[string]string{
		"1":  "1 (init) S 0 1 1 0 -1",
		"20": "20 (bash) S 1 20 20 0 -1",
		"30": "30 (weird (name) x) R 20 30 20 0 -1",
	}
	for pid, stat := range stats {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, pid, "stat"), []byte(stat+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Not processes: ignored.
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "40"), 0o755); err != nil { // exited: no stat
		t.Fatal(err)
	}

	children, ok := procChildPIDSnapshot(root)
	if !ok {
		t.Fatal("procChildPIDSnapshot found no processes")
	}
	for _, kids := range children {
		sort.Ints(kids)
	}
	want := map[int][]int{0: {1}, 1: {20}, 20: {30}}
	if !reflect.DeepEqual(children, want) {
		t.Errorf("children = %v, want %v", children, want)
	}

	if _, ok := procChildPIDSnapshot(filepath.Join(root, "missing")); ok {
		t.Error("an unreadable root must report !ok so the caller falls back to ps")
	}
}

// The real snapshot of this host sees this test process as its parent's child.
func TestProcChildPIDSnapshot_SeesThisProcess(t *testing.T) {
	children, ok := procChildPIDSnapshot(procRoot)
	if !ok {
		t.Skip("procfs not readable on this host")
	}
	for _, pid := range children[os.Getppid()] {
		if pid == os.Getpid() {
			return
		}
	}
	t.Errorf("pid %d not among the children of %d", os.Getpid(), os.Getppid())
}
