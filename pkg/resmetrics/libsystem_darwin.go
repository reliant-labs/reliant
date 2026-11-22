//go:build darwin

package resmetrics

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// The libSystem calls golang.org/x/sys/unix does not wrap: proc_pidinfo for
// the process's resident size and threads, and the mach host statistics (and
// the page size they count in) for the machine's CPU and memory. They are called the way x/sys calls every
// libSystem function, with no cgo: a trampoline in libsystem_darwin.s jumps
// to the symbol the linker imports dynamically (//go:cgo_import_dynamic), and
// the runtime's syscall.syscall6 makes the call on the system stack. So a
// CGO_ENABLED=0 build of any forge binary links them, as forge's own darwin
// release does.

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_proc_pidinfo_trampoline_addr uintptr

//go:cgo_import_dynamic libc_proc_pidinfo proc_pidinfo "/usr/lib/libSystem.B.dylib"

var libc_mach_host_self_trampoline_addr uintptr

//go:cgo_import_dynamic libc_mach_host_self mach_host_self "/usr/lib/libSystem.B.dylib"

var libc_host_statistics_trampoline_addr uintptr

//go:cgo_import_dynamic libc_host_statistics host_statistics "/usr/lib/libSystem.B.dylib"

var libc_host_statistics64_trampoline_addr uintptr

//go:cgo_import_dynamic libc_host_statistics64 host_statistics64 "/usr/lib/libSystem.B.dylib"

var libc_host_page_size_trampoline_addr uintptr

//go:cgo_import_dynamic libc_host_page_size host_page_size "/usr/lib/libSystem.B.dylib"

// <sys/proc_info.h> and <mach/host_info.h>.
const (
	procPidTaskInfo      = 4 // PROC_PIDTASKINFO
	hostCPULoadInfo      = 3 // HOST_CPU_LOAD_INFO
	hostCPULoadInfoCount = 4 // HOST_CPU_LOAD_INFO_COUNT, in integer_t
	hostVMInfo64         = 4 // HOST_VM_INFO64
	hostVMInfo64Count    = sizeofVMStatistics64 / 4
	kernSuccess          = 0
	machPortNull         = 0
)

// taskInfo reads proc_pidinfo(PROC_PIDTASKINFO) for this process.
func taskInfo() (rss, virt uint64, threads int64, err error) {
	buf := make([]byte, sizeofTaskInfo)
	n, _, errno := syscall_syscall6(libc_proc_pidinfo_trampoline_addr,
		uintptr(os.Getpid()), procPidTaskInfo, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	runtime.KeepAlive(buf)
	if got := int32(n); got <= 0 { //nolint:gosec // proc_pidinfo returns an int
		if errno != 0 {
			return 0, 0, 0, fmt.Errorf("proc_pidinfo: %w", errno)
		}
		return 0, 0, 0, fmt.Errorf("proc_pidinfo returned %d", got)
	}
	return decodeTaskInfo(buf[:min(int(int32(n)), len(buf))]) //nolint:gosec // bounded by the buffer
}

// machHost is this task's send right to its host port, taken once:
// mach_host_self adds a reference on every call.
var machHost = sync.OnceValues(func() (uint32, error) {
	r, _, _ := syscall_syscall6(libc_mach_host_self_trampoline_addr, 0, 0, 0, 0, 0, 0)
	if port := uint32(r); port != machPortNull { //nolint:gosec // mach_port_t is a uint32
		return port, nil
	}
	return 0, errors.New("mach_host_self returned MACH_PORT_NULL")
})

// hostStatistics fills info (count integer_t words) with one host statistics
// flavor; is64 selects host_statistics64.
func hostStatistics(flavor uintptr, info unsafe.Pointer, count uint32, is64 bool) error {
	host, err := machHost()
	if err != nil {
		return err
	}
	fn, name := libc_host_statistics_trampoline_addr, "host_statistics"
	if is64 {
		fn, name = libc_host_statistics64_trampoline_addr, "host_statistics64"
	}
	got := count
	r, _, _ := syscall_syscall6(fn, uintptr(host), flavor, uintptr(info), uintptr(unsafe.Pointer(&got)), 0, 0)
	if kr := int32(r); kr != kernSuccess { //nolint:gosec // kern_return_t is an int
		return fmt.Errorf("%s: kern_return_t %d", name, kr)
	}
	if got < count {
		return fmt.Errorf("%s: %d words, want %d", name, got, count)
	}
	return nil
}

// hostCPUTicks reads HOST_CPU_LOAD_INFO: the machine's ticks in each CPU
// state, summed over every CPU.
func hostCPUTicks() ([4]uint32, error) {
	var ticks [hostCPULoadInfoCount]uint32
	err := hostStatistics(hostCPULoadInfo, unsafe.Pointer(&ticks), hostCPULoadInfoCount, false)
	return ticks, err
}

// hostPageSize is the kernel's page size, the unit of the host VM
// statistics. It is not hw.pagesize: an x86 binary under Rosetta sees 4KiB
// pages there while the kernel counts 16KiB ones.
func hostPageSize() (uint64, error) {
	host, err := machHost()
	if err != nil {
		return 0, err
	}
	var size uint64 // vm_size_t
	r, _, _ := syscall_syscall6(libc_host_page_size_trampoline_addr, uintptr(host), uintptr(unsafe.Pointer(&size)), 0, 0, 0, 0)
	if kr := int32(r); kr != kernSuccess { //nolint:gosec // kern_return_t is an int
		return 0, fmt.Errorf("host_page_size: kern_return_t %d", kr)
	}
	if size == 0 {
		return 0, errors.New("host_page_size returned 0")
	}
	return size, nil
}

// hostVMStatistics64 reads HOST_VM_INFO64 as the raw structure.
func hostVMStatistics64() ([]byte, error) {
	buf := make([]byte, sizeofVMStatistics64)
	err := hostStatistics(hostVMInfo64, unsafe.Pointer(&buf[0]), hostVMInfo64Count, true)
	return buf, err
}
