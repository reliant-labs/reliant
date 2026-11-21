// Trampolines to the libSystem functions libsystem_darwin.go calls, in the
// form golang.org/x/sys/unix uses (zsyscall_darwin_*.s): the same text
// assembles for amd64 and arm64.

//go:build darwin

#include "textflag.h"

TEXT libc_proc_pidinfo_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_proc_pidinfo(SB)
GLOBL	·libc_proc_pidinfo_trampoline_addr(SB), RODATA, $8
DATA	·libc_proc_pidinfo_trampoline_addr(SB)/8, $libc_proc_pidinfo_trampoline<>(SB)

TEXT libc_mach_host_self_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_mach_host_self(SB)
GLOBL	·libc_mach_host_self_trampoline_addr(SB), RODATA, $8
DATA	·libc_mach_host_self_trampoline_addr(SB)/8, $libc_mach_host_self_trampoline<>(SB)

TEXT libc_host_statistics_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_host_statistics(SB)
GLOBL	·libc_host_statistics_trampoline_addr(SB), RODATA, $8
DATA	·libc_host_statistics_trampoline_addr(SB)/8, $libc_host_statistics_trampoline<>(SB)

TEXT libc_host_statistics64_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_host_statistics64(SB)
GLOBL	·libc_host_statistics64_trampoline_addr(SB), RODATA, $8
DATA	·libc_host_statistics64_trampoline_addr(SB)/8, $libc_host_statistics64_trampoline<>(SB)

TEXT libc_host_page_size_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_host_page_size(SB)
GLOBL	·libc_host_page_size_trampoline_addr(SB), RODATA, $8
DATA	·libc_host_page_size_trampoline_addr(SB)/8, $libc_host_page_size_trampoline<>(SB)
