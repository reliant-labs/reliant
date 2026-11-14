//go:build linux

package cli

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPortListenerPID_NeedsNoExternalTool pins the bug behind the
// TestRunUpServices_EndToEnd failures: portListenerPID shelled out to lsof,
// which most minimal Linux images (and the sandboxed cloud workspace) do not
// ship, so every holder resolved to pid 0 and `forge env status` reported no
// pid or ownership for its own stack. With PATH emptied no tool can be found,
// so this passes only if the kernel tables are read directly.
func TestPortListenerPID_NeedsNoExternalTool(t *testing.T) {
	t.Setenv("PATH", "")
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
		if err != nil {
			if strings.HasPrefix(addr, "[") {
				t.Logf("no IPv6 loopback here (%v); IPv4 still covered", err)
				continue
			}
			t.Fatalf("listen %s: %v", addr, err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		got := portListenerPID(port)
		_ = ln.Close()
		if got != os.Getpid() {
			t.Errorf("portListenerPID(%d) for %s = %d, want this test process %d", port, addr, got, os.Getpid())
		}
	}
}

// TestListenSocketInodes covers the /proc/net/tcp{,6} row parsing: only
// LISTEN rows on the requested port count, in either address family.
func TestListenSocketInodes(t *testing.T) {
	// Port 8090 = 0x1F9A. Rows: header; a LISTEN on 8090 (inode 111); an
	// ESTABLISHED connection whose local side is also 8090 (must not count);
	// a LISTEN on another port; a LISTEN on 8090 with inode 0 (a socket the
	// kernel will not attribute — must not count).
	const tcp4 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F9A 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F9A 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 222 1 0000000000000000 20 4 30 10 -1
   2: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 333 1 0000000000000000 100 0 0 10 0
   3: 00000000:1F9A 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 0 1 0000000000000000 100 0 0 10 0
`
	if got := listenSocketInodes(strings.NewReader(tcp4), 8090); len(got) != 1 || got[0] != 111 {
		t.Errorf("tcp4 inodes for :8090 = %v, want [111]", got)
	}
	if got := listenSocketInodes(strings.NewReader(tcp4), 9999); len(got) != 0 {
		t.Errorf("tcp4 inodes for :9999 = %v, want none", got)
	}

	const tcp6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1F9A 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 444 1 0000000000000000 100 0 0 10 0
`
	if got := listenSocketInodes(strings.NewReader(tcp6), 8090); len(got) != 1 || got[0] != 444 {
		t.Errorf("tcp6 inodes for :8090 = %v, want [444]", got)
	}
}

func TestSocketLinkInode(t *testing.T) {
	for _, tc := range []struct {
		link string
		ino  uint64
		ok   bool
	}{
		{"socket:[1930374]", 1930374, true},
		{"pipe:[1930374]", 0, false},
		{"anon_inode:[eventpoll]", 0, false},
		{"socket:[]", 0, false},
		{"socket:[12", 0, false},
		{"/dev/null", 0, false},
	} {
		ino, ok := socketLinkInode(tc.link)
		if ino != tc.ino || ok != tc.ok {
			t.Errorf("socketLinkInode(%q) = (%d, %v), want (%d, %v)", tc.link, ino, ok, tc.ino, tc.ok)
		}
	}
}

// TestPortListenerPIDIn_FixtureTree drives the whole resolution against a
// synthetic proc root: the listener's inode is found in net/tcp6 (net/tcp
// holds only a non-listening row), two processes hold it across a fork and
// the LOWEST pid wins (lsof's sorted order, which the lsof implementation
// took the first line of), and non-pid entries and unrelated sockets are
// ignored. A port nobody listens on resolves to 0.
func TestPortListenerPIDIn_FixtureTree(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(pid, fd, target string) {
		t.Helper()
		dir := filepath.Join(root, pid, "fd")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, fd)); err != nil {
			t.Fatal(err)
		}
	}
	const header = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	write("net/tcp", header+
		"   0: 0100007F:1F9A 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 999 1 0 20 4 30 10 -1\n")
	write("net/tcp6", header+
		"   0: 00000000000000000000000000000000:1F9A 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 555 1 0 100 0 0 10 0\n")
	link("9", "0", "/dev/null")
	link("9", "5", "socket:[999]") // the established row's socket — not the listener
	link("40", "3", "socket:[555]")
	link("120", "7", "socket:[555]") // forked child sharing the listener
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := portListenerPIDIn(root, 8090); got != 40 {
		t.Errorf("portListenerPIDIn(:8090) = %d, want 40 (lowest holder of the LISTEN socket)", got)
	}
	if got := portListenerPIDIn(root, 3000); got != 0 {
		t.Errorf("portListenerPIDIn(:3000) = %d, want 0 (nothing listening)", got)
	}
	if got := portListenerPIDIn(root, 0); got != 0 {
		t.Errorf("portListenerPIDIn(0) = %d, want 0", got)
	}
}
