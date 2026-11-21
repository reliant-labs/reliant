package codegen

import "strings"

// buildSafeStem returns stem, the snake-cased rpc in a born file name, with
// "_rpc" appended when its last words would make the go tool read the file
// as build-constrained.
//
// WHY. Go treats a file name ending in _<GOOS>, _<GOARCH> or _<GOOS>_<GOARCH>
// (before .go, and before _test.go) as an implicit build constraint. The rpc
// ListMaintenanceWindows was born as rpc_list_maintenance_windows.go, which
// compiles only on Windows: on Linux its *Service method silently fell back to
// the embedded Unimplemented handler, and its scaffold test never ran, so
// nothing went red (control-plane#2934). Appending a word that is neither an
// OS nor an arch keeps the name derived from the rpc alone — still total and
// reversible — and leaves every other rpc's name exactly as it was.
func buildSafeStem(stem string) string {
	words := strings.Split(stem, "_")
	last := words[len(words)-1]
	if goKnownOS[last] || goKnownArch[last] {
		return stem + "_rpc"
	}
	return stem
}

// goKnownOS and goKnownArch are the go tool's file-name constraint words
// (internal/syslist: knownOS and knownArch), including the ports it knows
// but does not ship, because it honours those in file names too.
var (
	goKnownOS = setOf("aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js",
		"linux", "nacl", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos")
	goKnownArch = setOf("386", "amd64", "amd64p32", "arm", "armbe", "arm64", "arm64be", "loong64", "mips",
		"mipsle", "mips64", "mips64le", "mips64p32", "mips64p32le", "ppc", "ppc64", "ppc64le", "riscv",
		"riscv64", "s390", "s390x", "sparc", "sparc64", "wasm")
)

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
