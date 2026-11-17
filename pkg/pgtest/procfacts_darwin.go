//go:build darwin

package pgtest

import (
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// readProcFacts reads pid's executable (KERN_PROCARGS2 exec_path) and start
// time (kern.proc.pid kinfo_proc). Mirrors internal/cli/procinspect_darwin.go.
func readProcFacts(pid int) procFacts {
	var f procFacts
	if pid <= 0 {
		return f
	}
	if kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid); err == nil && int(kp.Proc.P_pid) == pid {
		tv := kp.Proc.P_starttime
		if tv.Sec > 0 {
			f.start = time.Unix(int64(tv.Sec), int64(tv.Usec)*1000)
			f.startOK = true
		}
	}
	if path := procargs2ExecPath(pid); path != "" {
		f.exe, f.exeOK = path, true
	}
	return f
}

// procargs2ExecPath returns the exec_path at the head of the KERN_PROCARGS2
// block (int32 argc, then the NUL-terminated exec path), or "" if unreadable.
func procargs2ExecPath(pid int) string {
	const (
		ctlKern       = 1
		kernArgmax    = 8
		kernProcargs2 = 49
	)
	var argmax int32
	size := uintptr(4)
	max := 262144
	if sysctlRaw([]int32{ctlKern, kernArgmax}, (*[4]byte)(unsafe.Pointer(&argmax))[:], &size) && argmax > 0 {
		max = int(argmax)
	}
	buf := make([]byte, max)
	size = uintptr(len(buf))
	if !sysctlRaw([]int32{ctlKern, kernProcargs2, int32(pid)}, buf, &size) || size < 5 {
		return ""
	}
	p := 4
	for p < int(size) && buf[p] != 0 {
		p++
	}
	return string(buf[4:p])
}

func sysctlRaw(mib []int32, buf []byte, size *uintptr) bool {
	var bufp uintptr
	if len(buf) > 0 {
		bufp = uintptr(unsafe.Pointer(&buf[0]))
	}
	_, _, errno := unix.Syscall6(unix.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)), bufp, uintptr(unsafe.Pointer(size)), 0, 0)
	return errno == 0
}
