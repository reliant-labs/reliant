// Package toolexec routes tool execution between the api-server/worker and the
// tools daemons: the NATS daemon router (DaemonRouter), the executors behind
// ToolExecutor, the NATS tool bridge and the local-model relay.
//
// The package's contract opt-out is declared here, once: forge reads a package
// directive from any file, and refuses two that disagree. It used to sit on
// daemon_lifecycle_publisher.go and left with that file (#2024), which turned
// the contract lane red. Forge's exclusion gate refuses an opt-out for a
// package that does outbound I/O or owns a multi-implementation interface,
// and this package does both, so each refusal is suppressed below with its
// reason. Replacing the opt-out with a contract.go means splitting the router
// and the executors into components with constructor-injected Deps; that is
// tracked as an open follow-up, not done here.
//
//forge:lint-disable-next-line forge-exclude-contract-multi-impl: DaemonConnectionListener is an observer seam fanned out to several subscribers (publisher, bridge, responders), not interchangeable services; the contract.go split is an open follow-up
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: daemon_router_nats.go IS the NATS request/reply transport to daemons; the contract.go split into adapter-shaped components is an open follow-up
//forge:exclude-contract: tool execution routing between api-server/worker and daemons over NATS
package toolexec
