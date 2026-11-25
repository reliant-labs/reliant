// Copyright (c) 2026 Reliant Labs
//go:build !linux

package osutil

import "os"

// descendants takes nothing into custody outside Linux. A descendant can only
// be signalled safely through a handle that names it alone (a Linux pidfd);
// macOS has none, and a signal to a pid or a group id the command no longer
// holds may reach an unrelated process. So there the command's own group is
// signalled (terminateGracefully, killGroup) and the groups its descendants
// made of their own are not. Windows has no process groups to signal, and
// cancellation there is a kill of the child (terminateGracefully).
type descendants struct{}

func snapshotDescendants(int) *descendants { return &descendants{} }

func (*descendants) refresh()    {}
func (*descendants) terminate()  {}
func (*descendants) alive() bool { return false }
func (*descendants) release()    {}

// escalate is killGroup's alone.
func (*descendants) escalate(proc *os.Process, members []GroupMember) {
	killGroup(proc, members)
}
