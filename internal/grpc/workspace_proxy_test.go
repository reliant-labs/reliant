// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/handlers/background"
	"github.com/reliant-labs/reliant/internal/handlers/file_system"
	"github.com/reliant-labs/reliant/internal/handlers/package_commands"
	"github.com/reliant-labs/reliant/internal/handlers/terminal"
)

// Compile-time guard: every browser-facing workspace service that NewServer
// swaps for a daemon proxy when router != nil must have a proxy type that
// actually satisfies the corresponding Connect handler interface. If a future
// service is added to that branch without a working proxy, this block fails to
// compile — the failure mode we are defending against is a service silently
// left un-proxied (its filesystem work runs on the api-server and returns
// empty), which is exactly the PackageCommands bug.
var (
	_ reliantv1connect.FileSystemServiceHandler      = (*file_system.Service)(nil)
	_ reliantv1connect.BackgroundServiceHandler      = (*background.Service)(nil)
	_ reliantv1connect.TerminalServiceHandler        = (*terminal.Service)(nil)
	_ reliantv1connect.PackageCommandsServiceHandler = (*package_commands.Service)(nil)
)
