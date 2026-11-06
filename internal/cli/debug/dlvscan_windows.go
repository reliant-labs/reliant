//go:build windows

package debug

import (
	"context"
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dlvPIDsForAddr returns PIDs of dlv.exe processes whose command line contains
// --listen=<addr>. Windows has no `ps`: it enumerates processes with a
// Toolhelp32 snapshot, keeps those whose image is dlv.exe, and reads each
// one's command line via NtQueryInformationProcess(ProcessCommandLineInformation)
// so only THIS session's dlv matches, never an unrelated debugger. A process
// whose command line cannot be read (access denied, exited) is skipped.
func dlvPIDsForAddr(ctx context.Context, addr string) []int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	listen := "--listen=" + addr
	var pids []int
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if ctx.Err() != nil {
			return pids
		}
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "dlv.exe") {
			continue
		}
		if cmdlineHasListen(entry.ProcessID, listen) {
			pids = append(pids, int(entry.ProcessID))
		}
	}
	return pids
}

func cmdlineHasListen(pid uint32, listen string) bool {
	cmdline, err := processCommandLine(pid)
	return err == nil && strings.Contains(cmdline, listen)
}

// isDlvForAddr reports whether pid is currently a dlv.exe listening on addr.
// The image name comes from a fresh Toolhelp snapshot entry for exactly this
// pid, so a recycled pid running anything else is rejected.
func isDlvForAddr(ctx context.Context, pid int, addr string) bool {
	if pid <= 0 || addr == "" {
		return false
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if ctx.Err() != nil {
			return false
		}
		if int(entry.ProcessID) != pid {
			continue
		}
		return strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "dlv.exe") &&
			cmdlineHasListen(entry.ProcessID, "--listen="+addr)
	}
	return false
}

func processCommandLine(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(h) }()

	buf := make([]byte, 4096)
	for {
		var needed uint32
		err = windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation,
			unsafe.Pointer(&buf[0]), uint32(len(buf)), &needed)
		if err == nil {
			break
		}
		if (err == windows.STATUS_INFO_LENGTH_MISMATCH || err == windows.STATUS_BUFFER_OVERFLOW ||
			err == windows.STATUS_BUFFER_TOO_SMALL) && int(needed) > len(buf) {
			buf = make([]byte, needed)
			continue
		}
		return "", err
	}
	return decodeUnicodeString(buf)
}

// decodeUnicodeString decodes the UNICODE_STRING the kernel wrote at the head
// of buf. Its Buffer field is an absolute pointer into buf as it was when the
// syscall ran; it is converted to an offset and bounds-checked rather than
// dereferenced, so a stack copy of buf cannot leave it dangling.
func decodeUnicodeString(buf []byte) (string, error) {
	if len(buf) < int(unsafe.Sizeof(windows.NTUnicodeString{})) {
		return "", errBadUnicodeString
	}
	us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
	if us.Length == 0 || us.Buffer == nil {
		return "", errBadUnicodeString
	}
	return unicodeStringAt(buf, uintptr(unsafe.Pointer(us.Buffer))-uintptr(unsafe.Pointer(&buf[0])), us.Length)
}

var errBadUnicodeString = errors.New("malformed UNICODE_STRING")

// unicodeStringAt decodes length bytes of UTF-16 at byte offset off in buf.
func unicodeStringAt(buf []byte, off uintptr, length uint16) (string, error) {
	end := off + uintptr(length)
	if off > uintptr(len(buf)) || end < off || end > uintptr(len(buf)) || length%2 != 0 {
		return "", errBadUnicodeString
	}
	u16 := make([]uint16, length/2)
	for i := range u16 {
		u16[i] = uint16(buf[int(off)+2*i]) | uint16(buf[int(off)+2*i+1])<<8
	}
	return windows.UTF16ToString(u16), nil
}
