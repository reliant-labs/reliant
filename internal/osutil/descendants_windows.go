// Copyright (c) 2026 Reliant Labs
//go:build windows

package osutil

// descendants is empty on Windows: there are no process groups to signal, and
// cancellation there is a kill of the child (terminateGracefully).
type descendants struct{}

func snapshotDescendants(int) *descendants { return &descendants{} }

func (*descendants) refresh()    {}
func (*descendants) terminate()  {}
func (*descendants) kill()       {}
func (*descendants) alive() bool { return false }
