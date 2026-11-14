//go:build windows

package cli

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Layout offsets on 64-bit Windows (amd64 and arm64 — forge ships no 386
// build, asserted below). Primary source: System Informer's phnt headers,
// ntrtl.h (struct _RTL_USER_PROCESS_PARAMETERS: Environment follows
// CommandLine; EnvironmentSize follows the 32-entry CurrentDirectories array
// and the trailing ULONG block) and ntpebteb.h (PEB.ProcessParameters).
// Hand-checked against the struct layout: Environment = 0x80,
// EnvironmentSize = 0x3F0, PEB.ProcessParameters = 0x20 (matches Geoff
// Chappell's tables). EnvironmentSize exists since Vista.
const (
	pebProcessParametersOffset  = 0x20
	rtlParamsEnvironmentOffset  = 0x80
	rtlParamsEnvSizeOffset      = 0x3F0
	maxEnvironBytes             = 1 << 20
	processWow64InformationInfo = 26
	rtlParamsProcessGroupOffset = 0x408
)

// Compile-time guards: the x/sys structs mirror the same layout, so a
// mismatch (or a 32-bit build) fails the build instead of reading garbage.
var modNtdll = windows.NewLazySystemDLL("ntdll.dll")
var procNtQueryInfoProcess = modNtdll.NewProc("NtQueryInformationProcess")

var (
	_ [1 - 2*(unsafe.Sizeof(uintptr(0))/8^1)]struct{}
	_ [unsafe.Offsetof(windows.PEB{}.ProcessParameters) - pebProcessParametersOffset]struct{}
	_ [pebProcessParametersOffset - unsafe.Offsetof(windows.PEB{}.ProcessParameters)]struct{}
	_ [unsafe.Offsetof(windows.RTL_USER_PROCESS_PARAMETERS{}.Environment) - rtlParamsEnvironmentOffset]struct{}
	_ [rtlParamsEnvironmentOffset - unsafe.Offsetof(windows.RTL_USER_PROCESS_PARAMETERS{}.Environment)]struct{}
	_ [unsafe.Offsetof(windows.RTL_USER_PROCESS_PARAMETERS{}.ProcessGroupId) - rtlParamsProcessGroupOffset]struct{}
	_ [rtlParamsProcessGroupOffset - unsafe.Offsetof(windows.RTL_USER_PROCESS_PARAMETERS{}.ProcessGroupId)]struct{}
	_ [unsafe.Offsetof(windows.RTL_USER_PROCESS_PARAMETERS{}.EnvironmentSize) - rtlParamsEnvSizeOffset]struct{}
	_ [rtlParamsEnvSizeOffset - unsafe.Offsetof(windows.RTL_USER_PROCESS_PARAMETERS{}.EnvironmentSize)]struct{}
)

// readProcEnviron reads pid's environment block out of its PEB:
// OpenProcess -> NtQueryInformationProcess(ProcessBasicInformation) ->
// ReadProcessMemory(PEB.ProcessParameters) -> Environment/EnvironmentSize ->
// ReadProcessMemory(environment). Every failure yields (nil, false) so an
// unidentifiable process is treated as foreign and never reclaimed.
//
// WOW64 (32-bit) targets are reported unreadable: their PEB32 uses different
// offsets and forge-launched services are 64-bit. x64 processes emulated on
// arm64 carry a normal 64-bit PEB and are read with the offsets above.
// Note this reads the environment as of process start (plus in-process
// SetEnvironmentVariable changes, which update the same block).
func readProcEnviron(pid int) ([]string, bool) {
	if pid <= 0 {
		return nil, false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return nil, false
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var wow64 uintptr
	var retLen uint32
	if err := windows.NtQueryInformationProcess(h, processWow64InformationInfo,
		unsafe.Pointer(&wow64), uint32(unsafe.Sizeof(wow64)), &retLen); err != nil || wow64 != 0 {
		return nil, false
	}

	var pbi windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(h, 0, unsafe.Pointer(&pbi),
		uint32(unsafe.Sizeof(pbi)), &retLen); err != nil || pbi.PebBaseAddress == nil {
		return nil, false
	}

	var paramsAddr uintptr
	if !readRemote(h, uintptr(unsafe.Pointer(pbi.PebBaseAddress))+pebProcessParametersOffset,
		unsafe.Pointer(&paramsAddr), unsafe.Sizeof(paramsAddr)) || paramsAddr == 0 {
		return nil, false
	}
	var envAddr, envSize uintptr
	if !readRemote(h, paramsAddr+rtlParamsEnvironmentOffset, unsafe.Pointer(&envAddr), unsafe.Sizeof(envAddr)) ||
		!readRemote(h, paramsAddr+rtlParamsEnvSizeOffset, unsafe.Pointer(&envSize), unsafe.Sizeof(envSize)) {
		return nil, false
	}
	if envAddr == 0 || envSize < 4 || envSize > maxEnvironBytes {
		return nil, false
	}
	envSize &^= 1 // whole UTF-16 units
	buf := make([]uint16, envSize/2)
	if !readRemote(h, envAddr, unsafe.Pointer(&buf[0]), envSize) {
		return nil, false
	}

	var out []string
	start := 0
	for i, c := range buf {
		if c != 0 {
			continue
		}
		if i == start { // empty entry: double-NUL terminator
			break
		}
		out = append(out, windows.UTF16ToString(buf[start:i]))
		start = i + 1
	}
	return out, true
}

// readRemote fills size bytes at dst from addr in h, requiring a full read.
func readRemote(h windows.Handle, addr uintptr, dst unsafe.Pointer, size uintptr) bool {
	var n uintptr
	if err := windows.ReadProcessMemory(h, addr, (*byte)(dst), size, &n); err != nil {
		return false
	}
	return n == size
}

// readProcArgv reads the command line via NtQueryInformationProcess
// (ProcessCommandLineInformation = 60, Windows 8.1+), which needs only
// PROCESS_QUERY_LIMITED_INFORMATION and no PEB walking.
func readProcArgv(pid int) ([]string, bool) {
	if pid <= 0 {
		return nil, false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, false
	}
	defer func() { _ = windows.CloseHandle(h) }()

	const class = 60
	buf := make([]byte, 1024)
	for attempt := 0; attempt < 4; attempt++ {
		var retLen uint32
		r, _, _ := procNtQueryInfoProcess.Call(uintptr(h), class, uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)), uintptr(unsafe.Pointer(&retLen)))
		status := uint32(r)
		switch status {
		case 0:
			us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
			off, ok := ntStringOffset(uintptr(unsafe.Pointer(us.Buffer)), uintptr(unsafe.Pointer(&buf[0])),
				us.Length, uintptr(len(buf)), unsafe.Sizeof(*us))
			if us.Length == 0 || !ok {
				return nil, false
			}
			cmdline := decodeUTF16LE(buf[off : off+uintptr(us.Length)])
			argv, err := windows.DecomposeCommandLine(cmdline)
			if err != nil || len(argv) == 0 {
				return nil, false
			}
			return argv, true
		case 0xC0000004, 0x80000005, 0xC0000023: // length mismatch / overflow / too small
			if int(retLen) <= len(buf) {
				return nil, false
			}
			buf = make([]byte, retLen)
		default:
			return nil, false
		}
	}
	return nil, false
}

// procExecPath returns the full image path via QueryFullProcessImageNameW.
func procExecPath(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", false
	}
	return windows.UTF16ToString(buf[:n]), true
}

// readProcGroupID reads RTL_USER_PROCESS_PARAMETERS.ProcessGroupId through
// the same PEB walk as readProcEnviron. A group leader created with
// CREATE_NEW_PROCESS_GROUP has ProcessGroupId == its own pid. Any failure
// yields (0, false).
func readProcGroupID(pid int) (uint32, bool) {
	if pid <= 0 {
		return 0, false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var wow64 uintptr
	var retLen uint32
	if err := windows.NtQueryInformationProcess(h, processWow64InformationInfo,
		unsafe.Pointer(&wow64), uint32(unsafe.Sizeof(wow64)), &retLen); err != nil || wow64 != 0 {
		return 0, false
	}
	var pbi windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(h, 0, unsafe.Pointer(&pbi),
		uint32(unsafe.Sizeof(pbi)), &retLen); err != nil || pbi.PebBaseAddress == nil {
		return 0, false
	}
	var paramsAddr uintptr
	if !readRemote(h, uintptr(unsafe.Pointer(pbi.PebBaseAddress))+pebProcessParametersOffset,
		unsafe.Pointer(&paramsAddr), unsafe.Sizeof(paramsAddr)) || paramsAddr == 0 {
		return 0, false
	}
	var group uint32
	if !readRemote(h, paramsAddr+rtlParamsProcessGroupOffset, unsafe.Pointer(&group), unsafe.Sizeof(group)) {
		return 0, false
	}
	return group, true
}
