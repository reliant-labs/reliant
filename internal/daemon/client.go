// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:lint-disable-next-line forge-exclude-contract-multi-impl: daemon.Client is the long-standing local/remote strategy seam every tool executor takes; moving it into contract.go is a cross-package refactor with no generated payoff in a non-forge repo; tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: the daemon command/filesystem client (local in-process vs remote over the gateway)
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
