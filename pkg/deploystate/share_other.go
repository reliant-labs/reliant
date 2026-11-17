//go:build !windows

package deploystate

// isTransientShareErr is always false off Windows: rename(2) replaces a file
// atomically under any number of open readers, so there is no transient
// sharing failure to retry. See share_windows.go.
func isTransientShareErr(error) bool { return false }
