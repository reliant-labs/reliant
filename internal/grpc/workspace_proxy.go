// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// pickPackageCommandsService selects the PackageCommands implementation for the
// current topology: on a cloud daemon (router != nil) command discovery must run
// on the daemon's filesystem, so use the proxy; otherwise the DB/local service.
//
// The FileSystem/Background/Terminal workspace services make the same router !=
// nil switch inline in NewServer. Keeping PackageCommands' switch here makes it
// unit-testable (TestPickPackageCommandsService) and gives the browser-facing
// workspace-proxy guard test a concrete seam to pin.
func pickPackageCommandsService(
	router toolexec.DaemonRouter,
	database db.Repository,
) reliantv1connect.PackageCommandsServiceHandler {
	if router != nil {
		return services.NewPackageCommandsProxyService(router, database)
	}
	return services.NewPackageCommandsService(database)
}
