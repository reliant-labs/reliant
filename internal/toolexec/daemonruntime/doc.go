// Copyright (c) 2025 Reliant Labs
//
// Package daemonruntime is the tools daemon's runtime: the command handlers
// behind the daemon command dispatch table (command_registry.go), the gateway
// session, and the process, terminal and preview streams they serve. Every
// collaborator is injected by the daemon runtime.
//
// The package's contract opt-out is declared here, once: forge reads a package
// directive from any file, and refuses two that disagree.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: the only dial is preview_forwarder.go's LOOPBACK dial to the user's own dev server (127.0.0.1/::1), which is the preview feature itself, not a third-party boundary
//forge:exclude-contract: the tools-daemon command handlers; shaped by the daemon command dispatch table, and every collaborator is injected by the daemon runtime
package daemonruntime
