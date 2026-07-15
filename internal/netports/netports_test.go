// Copyright (c) 2025 Reliant Labs
package netports

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Fixture lines captured from a real Linux /proc/net/tcp{,6} (trimmed
// columns are irrelevant to the parser, which only reads fields 1 and 3).
const tcp4Fixture = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:14EE 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:23E2 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0201A8C0:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000000000000000 100 0 0 10 0
   3: 0100007F:0016 0100007F:B123 01 00000000:00000000 00:00000000 00000000  1000        0 12348 1 0000000000000000 100 0 0 10 0
   4: 7F00007F:1B58 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12349 1 0000000000000000 100 0 0 10 0
`

// Row 0: vite on [::1]:5174 — the original production case.
// Row 1: wildcard [::]:8080.
// Row 2: same port 5358 as a v4 loopback listener (dedupe check).
// Row 3: non-loopback global v6 address (must be skipped).
const tcp6Fixture = `  sl  local_address                         rem_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1436 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 22345 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 22346 1 0000000000000000 100 0 0 10 0
   2: 00000000000000000000000001000000:14EE 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 22347 1 0000000000000000 100 0 0 10 0
   3: B80D01200000000000000000B9C60A08:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 22348 1 0000000000000000 100 0 0 10 0
`

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListeningLoopbackPorts_ParsesAndDedupes(t *testing.T) {
	tcp4 := writeFixture(t, "tcp", tcp4Fixture)
	tcp6 := writeFixture(t, "tcp6", tcp6Fixture)

	ports, ok := listeningLoopbackPorts(tcp4, tcp6, "", nil)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	// Expected:
	//   5358 (0x14EE) — 127.0.0.1 v4 LISTEN, deduped with ::1 v6 row 2
	//   9186 (0x23E2) — 0.0.0.0 wildcard v4
	//   7000 (0x1B58) — 127.0.0.127 (any 127/8 counts as loopback)
	//   5174 (0x1436) — ::1 v6 (the real-world vite case)
	//   8080 (0x1F90) — [::] wildcard v6; the 192.168.1.2:8080 v4 row and the
	//                    global-address v6 :80 row are skipped
	want := []uint32{5174, 5358, 7000, 8080, 9186}
	if !slices.Equal(ports, want) {
		t.Errorf("ports = %v, want %v", ports, want)
	}
	// Port 22 (0x0016) is ESTABLISHED (st 01), never LISTEN — must be absent.
	if slices.Contains(ports, 22) {
		t.Error("non-LISTEN socket leaked into results")
	}
}

func TestListeningLoopbackPorts_Excludes(t *testing.T) {
	tcp4 := writeFixture(t, "tcp", tcp4Fixture)
	tcp6 := writeFixture(t, "tcp6", tcp6Fixture)

	ports, ok := listeningLoopbackPorts(tcp4, tcp6, "", map[int]bool{8080: true, 9186: true})
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []uint32{5174, 5358, 7000}
	if !slices.Equal(ports, want) {
		t.Errorf("ports = %v, want %v", ports, want)
	}
}

func TestListeningLoopbackPorts_MissingTables(t *testing.T) {
	dir := t.TempDir()
	_, ok := listeningLoopbackPorts(filepath.Join(dir, "nope"), filepath.Join(dir, "nope6"), "", nil)
	if ok {
		t.Error("ok = true with no proc tables, want false (non-Linux degradation)")
	}
}

func TestListeningLoopbackPorts_OneTablePresent(t *testing.T) {
	tcp4 := writeFixture(t, "tcp", tcp4Fixture)
	ports, ok := listeningLoopbackPorts(tcp4, filepath.Join(t.TempDir(), "nope6"), "", nil)
	if !ok {
		t.Fatal("ok = false with tcp4 table present")
	}
	want := []uint32{5358, 7000, 9186}
	if !slices.Equal(ports, want) {
		t.Errorf("ports = %v, want %v", ports, want)
	}
}

func TestListeningLoopbackPorts_MalformedLines(t *testing.T) {
	malformed := `header
   0: garbage
   1: 0100007F:ZZZZ 00000000:0000 0A x
   2: 0100007F 00000000:0000 0A x
   3: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1
`
	tcp4 := writeFixture(t, "tcp", malformed)
	ports, ok := listeningLoopbackPorts(tcp4, filepath.Join(t.TempDir(), "nope6"), "", nil)
	if !ok {
		t.Fatal("ok = false")
	}
	want := []uint32{8080}
	if !slices.Equal(ports, want) {
		t.Errorf("ports = %v, want %v (malformed lines skipped, valid line kept)", ports, want)
	}
}

func TestListeningLoopbackPorts_SkipsEphemeralRange(t *testing.T) {
	// 0x8181 = 33153 and 0xB571 = 46449: auto-assigned loopback listeners
	// (language servers, tool RPC sockets) inside the default ephemeral
	// range, as observed on a real workspace pod. 0x1436 = 5174 stays.
	fixture := `header
   0: 0100007F:1436 00000000:0000 0A 0
   1: 0100007F:8181 00000000:0000 0A 0
   2: 0100007F:B571 00000000:0000 0A 0
`
	tcp4 := writeFixture(t, "tcp", fixture)

	// Default range (missing ip_local_port_range file → 32768-60999).
	ports, ok := listeningLoopbackPorts(tcp4, filepath.Join(t.TempDir(), "nope6"), filepath.Join(t.TempDir(), "nope-range"), nil)
	if !ok {
		t.Fatal("ok = false")
	}
	if want := []uint32{5174}; !slices.Equal(ports, want) {
		t.Errorf("ports = %v, want %v (ephemeral-range listeners filtered)", ports, want)
	}

	// Custom kernel range that additionally covers 5174.
	rangeFile := writeFixture(t, "ip_local_port_range", "5000\t61000\n")
	ports, ok = listeningLoopbackPorts(tcp4, filepath.Join(t.TempDir(), "nope6"), rangeFile, nil)
	if !ok {
		t.Fatal("ok = false with range file")
	}
	if len(ports) != 0 {
		t.Errorf("ports = %v, want none (custom range covers all fixtures)", ports)
	}
}
