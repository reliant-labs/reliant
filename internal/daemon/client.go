package daemon

// Client provides access to filesystem and execution primitives on a user's machine.
// This is the core abstraction for the cloud split — tools use this interface
// instead of calling os.*/exec directly, enabling both local and remote execution.
//
// Two implementations:
//   - LocalClient: direct os.*/exec calls (monolith mode or running on daemon itself)
//   - RemoteClient: proxies via DaemonRouter.SendDaemonCommand (cloud/distributed mode)
type Client interface {
	FileSystem
	Executor
}

// Compile-time check that LocalClient implements Client.
var _ Client = (*LocalClient)(nil)
