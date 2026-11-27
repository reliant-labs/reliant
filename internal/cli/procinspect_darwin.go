//go:build darwin

package cli

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS sysctl MIB constants for reading another process's argument /
// environment block. They are not all exported by golang.org/x/sys/unix
// across versions, so the load-bearing ones are pinned here as raw values
// (stable kernel ABI): CTL_KERN=1, KERN_ARGMAX=8, KERN_PROCARGS2=49.
const (
	ctlKern       = 1
	kernArgmax    = 8
	kernProcargs2 = 49
)

// readProcEnviron returns pid's environment as KEY=VALUE strings on macOS.
// Thin wrapper over readProcArgs2 (which also yields the exec path and argv).
//
// ok is false when the block is unreadable (dead pid, permission, or a
// SIP-redacted system binary whose env the kernel withholds). Callers
// treat an unreadable holder as NOT-forge-owned, preserving the safety
// property that an unidentifiable process is never reclaimed.
func readProcEnviron(pid int) ([]string, bool) {
	_, _, env, ok := readProcArgs2(pid)
	return env, ok
}

// readProcArgv returns pid's argv on macOS — the command line the process was
// executed with. Used by `forge env status` duplicate attribution to tell two
// processes carrying the SAME ownership marker apart by what they are actually
// running (`reliant server worker` vs `reliant server api`).
//
// ok is false when the block is unreadable, which callers must surface as
// "could not determine attribution" rather than collapsing into a guess.
func readProcArgv(pid int) ([]string, bool) {
	_, argv, _, ok := readProcArgs2(pid)
	if !ok || len(argv) == 0 {
		return nil, false
	}
	return argv, true
}

// procExecPath returns the absolute path of the binary pid is executing on
// macOS — the exec_path the kernel records in the KERN_PROCARGS2 block. Used
// by `forge env status` build-freshness to show which binary a host service's
// server process is actually running (e.g. air's `tmp/main`). ok is false when
// the block is unreadable.
func procExecPath(pid int) (string, bool) {
	path, _, _, ok := readProcArgs2(pid)
	if !ok || path == "" {
		return "", false
	}
	return path, true
}

// readProcArgs2 reads a SAME-UID process's argv+env block via the macOS
// KERN_PROCARGS2 sysctl and returns its exec path, argv, and environment.
//
// macOS has no /proc, and modern `ps -E` / `ps e` no longer surface the
// environment (System Integrity Protection redacts it for platform
// binaries). The kernel does, however, still hand back the argv+env block
// for a SAME-UID process — which is exactly our case: forge is inspecting the
// user-space processes (`go run`, `air`, `node`) it launched itself. The block
// layout is:
//
//	int32 argc
//	char  exec_path[]   \0        <- the binary being executed
//	char  padding[]     (0+ NULs to alignment)
//	char  argv[0..argc-1][] each \0-terminated
//	char  env[...][]         each \0-terminated  <- the environment
//
// ok is false when the block is unreadable (dead pid, permission, or a
// SIP-redacted system binary whose env the kernel withholds).
func readProcArgs2(pid int) (execPath string, argv, env []string, ok bool) {
	if pid <= 0 {
		return "", nil, nil, false
	}
	// Size the buffer to the kernel's advertised maximum arg size. The
	// KERN_PROCARGS2 size-probe (NULL oldp) under-reports — it returns just
	// enough for argc+argv and clips the env — so KERN_ARGMAX is used to
	// allocate a buffer big enough to hold the whole block in one read.
	max := kernArgmaxValue()
	buf := make([]byte, max)
	size := uintptr(len(buf))
	mib := []int32{ctlKern, kernProcargs2, int32(pid)}
	if !sysctlRaw(mib, buf, &size) {
		return "", nil, nil, false
	}
	n := int(size)
	if n < 4 {
		return "", nil, nil, false
	}
	argc := int(*(*int32)(unsafe.Pointer(&buf[0])))
	p := 4
	// The exec path runs from just after argc up to its terminating NUL.
	pathStart := p
	for p < n && buf[p] != 0 {
		p++
	}
	execPath = string(buf[pathStart:p])
	// Skip the exec path's NUL and any alignment padding NULs.
	for p < n && buf[p] == 0 {
		p++
	}
	// Walk the NUL-terminated strings: the first argc are argv, the rest
	// are the environment.
	seen := 0
	start := p
	for p < n {
		if buf[p] == 0 {
			s := string(buf[start:p])
			if seen < argc {
				// argv entries are positional: an empty argument is a real
				// argument and must not be dropped, or every later index
				// shifts.
				argv = append(argv, s)
			} else if s != "" {
				env = append(env, s)
			}
			seen++
			p++
			start = p
			continue
		}
		p++
	}
	return execPath, argv, env, true
}

// kernArgmaxValue reads kern.argmax (the max size of the arg/env block),
// falling back to the historical 256 KiB default if the sysctl fails.
func kernArgmaxValue() int {
	var v int32
	size := uintptr(4)
	mib := []int32{ctlKern, kernArgmax}
	if !sysctlRaw(mib, (*[4]byte)(unsafe.Pointer(&v))[:], &size) || v <= 0 {
		return 262144
	}
	return int(v)
}

// sysctlRaw performs a raw sysctl(mib, buf, &size) via the darwin syscall,
// writing up to len(buf) bytes into buf and setting *size to the byte count
// actually written. Returns false on any errno.
func sysctlRaw(mib []int32, buf []byte, size *uintptr) bool {
	var bufp uintptr
	if len(buf) > 0 {
		bufp = uintptr(unsafe.Pointer(&buf[0]))
	}
	_, _, errno := unix.Syscall6(
		unix.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)),
		bufp,
		uintptr(unsafe.Pointer(size)),
		0, 0,
	)
	return errno == 0
}
