package cli

import "encoding/binary"

// tcpRowLayout is the byte layout of one row of a GetExtendedTcpTable
// TCP_TABLE_OWNER_PID_* table. Pure data, untagged, so the parser is tested on
// every OS against the documented layouts even though only Windows reads a
// real table (procgroup_windows.go).
type tcpRowLayout struct {
	size, portOff, pidOff int
}

// tcp4RowLayout is MIB_TCPROW_OWNER_PID (tcpmib.h), six DWORDs:
// dwState 0, dwLocalAddr 4, dwLocalPort 8, dwRemoteAddr 12, dwRemotePort 16,
// dwOwningPid 20.
var tcp4RowLayout = tcpRowLayout{size: 24, portOff: 8, pidOff: 20}

// tcp6RowLayout is MIB_TCP6ROW_OWNER_PID (tcpmib.h): ucLocalAddr[16] 0,
// dwLocalScopeId 16, dwLocalPort 20, ucRemoteAddr[16] 24, dwRemoteScopeId 40,
// dwRemotePort 44, dwState 48, dwOwningPid 52.
var tcp6RowLayout = tcpRowLayout{size: 56, portOff: 20, pidOff: 52}

// scanListenerTable returns the owning pid of the row whose local port is
// port, or 0. buf is the table: a DWORD row count, then the rows.
//
// The port is a DWORD in NETWORK byte order in its low 16 bits: port 8080
// (0x1F90) is stored as bytes 1F 90 00 00, which reads little-endian as
// 0x0000901F — so the two low bytes are swapped back.
func scanListenerTable(buf []byte, port int, layout tcpRowLayout) int {
	if len(buf) < 4 {
		return 0
	}
	n := int(binary.LittleEndian.Uint32(buf))
	for i := 0; i < n; i++ {
		start := 4 + i*layout.size
		if start+layout.size > len(buf) {
			return 0
		}
		row := buf[start : start+layout.size]
		raw := binary.LittleEndian.Uint32(row[layout.portOff:])
		rowPort := int(raw&0xff)<<8 | int(raw>>8&0xff)
		if rowPort == port {
			return int(binary.LittleEndian.Uint32(row[layout.pidOff:]))
		}
	}
	return 0
}
