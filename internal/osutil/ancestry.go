// Copyright (c) 2025 Reliant Labs
package osutil

import "os"

// maxAncestryDepth bounds the parent walk. Real process trees are a few dozen
// deep at most; the bound only guards against a cycle in what the OS reports
// while PIDs are being recycled underneath the walk.
const maxAncestryDepth = 256

// IsSelfOrAncestor reports whether pid is this process or one of its
// ancestors — that is, whether signalling pid would end the process asking.
//
// It answers false when the OS cannot say (an unsupported platform, a parent
// that exited mid-walk): callers use it to refuse a destructive action, and an
// unknown ancestry is not evidence of one.
func IsSelfOrAncestor(pid int) bool {
	if pid <= 0 {
		return false
	}
	current := os.Getpid()
	for range maxAncestryDepth {
		if current == pid {
			return true
		}
		if current <= 1 {
			return false
		}
		parent, err := ParentPID(current)
		if err != nil || parent == current {
			return false
		}
		current = parent
	}
	return false
}
