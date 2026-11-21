// Package writeguard fails the build when forge's own code writes a file with
// os.WriteFile.
//
// os.WriteFile truncates the target before it writes it, so a process killed in
// between — a sandbox restart in the middle of `forge generate` — leaves an empty
// or partial file. Twenty-seven generated middleware_gen.go files were left
// 0 bytes that way. Every file forge writes goes through
// internal/atomicfile.WriteFile (temp file, fsync, rename) instead.
//
// The guard is the test in writeguard_test.go. There is no runtime code here on
// purpose: the guard must never become something production code can call, tune
// or disable.
package writeguard
