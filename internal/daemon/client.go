// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package daemon

// Client provides access to filesystem and execution primitives on a user's machine.
// This is the core abstraction for the cloud split — tools use this interface
// instead of calling os.*/exec directly, enabling both local and remote execution.
//
// Two implementations:
//   - LocalClient: direct os.*/exec calls (running on daemon itself)
//   - RemoteClient: proxies via DaemonRouter.SendDaemonCommand (cloud/distributed mode)
type Client interface {
	FileSystem
	Executor
}

// Compile-time check that LocalClient implements Client.
var _ Client = (*LocalClient)(nil)
