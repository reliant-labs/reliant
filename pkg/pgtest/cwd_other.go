//go:build !windows

package pgtest

// releaseCallerCwd is a no-op off Windows: pg_ctl execs the postmaster, which
// chdirs into its data directory, so nothing the server leaves running holds
// the caller's working directory — and a POSIX working directory does not
// block removal anyway. See cwd_windows.go.
func releaseCallerCwd(string, uint32, map[string]string) error { return nil }
