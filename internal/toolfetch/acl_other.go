//go:build !windows

package toolfetch

// platformACL is the zero aclSource: a host without Windows ACLs reads none,
// and its owner and mode bits are checked instead (modesEnforced).
func platformACL() aclSource { return aclSource{} }
