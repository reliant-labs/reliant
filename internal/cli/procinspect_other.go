//go:build !darwin && !linux && !windows

package cli

// readProcEnviron has no portable implementation outside darwin/linux
// (BSDs, etc.; Windows has procinspect_windows.go). It always reports "unreadable", so ownership resolution
// degrades to "unidentifiable → treated as foreign" — the forge dev loop
// (`forge env up`) is Unix-first, and this keeps the build green elsewhere
// without ever misclassifying a process as reclaimable.
func readProcEnviron(_ int) ([]string, bool) {
	return nil, false
}

// readProcArgv has no portable implementation outside darwin/linux. It always
// reports "unreadable", so `forge env status` duplicate attribution degrades to
// the explicit "attribution undetermined" report rather than guessing a row —
// a confidently wrong attribution is worse than an admitted unknown.
func readProcArgv(_ int) ([]string, bool) {
	return nil, false
}

// procExecPath has no portable implementation outside darwin/linux. It always
// reports "unknown", so `forge env status` build-freshness degrades to
// health-only (no binary path / mtime) without misfiring.
func procExecPath(_ int) (string, bool) {
	return "", false
}
