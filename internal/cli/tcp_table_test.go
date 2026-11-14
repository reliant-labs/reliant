package cli

import (
	"encoding/binary"
	"testing"
)

// tcp4Row builds one MIB_TCPROW_OWNER_PID exactly as Windows lays it out,
// field by field, so the test does not share the parser's offsets.
func tcp4Row(state, localAddr uint32, localPort uint16, pid uint32) []byte {
	row := make([]byte, 24)
	binary.LittleEndian.PutUint32(row[0:], state)
	binary.LittleEndian.PutUint32(row[4:], localAddr)
	binary.BigEndian.PutUint16(row[8:], localPort) // network byte order
	binary.LittleEndian.PutUint32(row[20:], pid)
	return row
}

// tcp6Row builds one MIB_TCP6ROW_OWNER_PID.
func tcp6Row(localPort uint16, state, pid uint32) []byte {
	row := make([]byte, 56)
	row[15] = 1                                     // ucLocalAddr = ::1
	binary.BigEndian.PutUint16(row[20:], localPort) // dwLocalPort
	binary.LittleEndian.PutUint32(row[48:], state)
	binary.LittleEndian.PutUint32(row[52:], pid)
	return row
}

func table(rows ...[]byte) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(len(rows)))
	for _, r := range rows {
		buf = append(buf, r...)
	}
	return buf
}

const listenState = 2 // MIB_TCP_STATE_LISTEN

// TestScanListenerTableIPv4 pins the IPv4 layout. The port is at offset 8;
// an earlier version read offset 4 (dwLocalAddr), so a listener on 0.0.0.0
// matched nothing and every IPv4 port on Windows looked foreign to `env up`.
func TestScanListenerTableIPv4(t *testing.T) {
	buf := table(
		tcp4Row(listenState, 0, 5432, 111),          // 0.0.0.0:5432
		tcp4Row(listenState, 0x0100007f, 8080, 222), // 127.0.0.1:8080
	)
	for _, tt := range []struct{ port, want int }{
		{5432, 111},
		{8080, 222},
		{9999, 0},
		{0, 0}, // the old bug's only "match": the local address of 0.0.0.0
	} {
		if got := scanListenerTable(buf, tt.port, tcp4RowLayout); got != tt.want {
			t.Errorf("port %d: pid = %d, want %d", tt.port, got, tt.want)
		}
	}
}

func TestScanListenerTableIPv6(t *testing.T) {
	buf := table(tcp6Row(3000, listenState, 333), tcp6Row(8443, listenState, 444))
	if got := scanListenerTable(buf, 8443, tcp6RowLayout); got != 444 {
		t.Fatalf("port 8443: pid = %d, want 444", got)
	}
	if got := scanListenerTable(buf, 3000, tcp6RowLayout); got != 333 {
		t.Fatalf("port 3000: pid = %d, want 333", got)
	}
}

// TestScanListenerTableTruncated: a row count larger than the buffer must
// stop, not read past the end.
func TestScanListenerTableTruncated(t *testing.T) {
	buf := table(tcp4Row(listenState, 0, 80, 7))
	binary.LittleEndian.PutUint32(buf, 50)
	if got := scanListenerTable(buf, 81, tcp4RowLayout); got != 0 {
		t.Fatalf("got %d from a truncated table", got)
	}
}
