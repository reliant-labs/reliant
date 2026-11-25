// Copyright (c) 2026 Reliant Labs
//go:build windows

package osutil

// strayGroups is empty on Windows: there are no process groups to signal, and
// cancellation there is a kill of the child (terminateGracefully).
type strayGroups struct{}

func snapshotStrayGroups(int) strayGroups { return strayGroups{} }

func (strayGroups) terminate()  {}
func (strayGroups) kill()       {}
func (strayGroups) alive() bool { return false }
