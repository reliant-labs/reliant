// Copyright (c) 2025 Reliant Labs
package package_commands

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

// ============================================================================
// Helper Methods
// ============================================================================

// convertCommandToProto converts a pkgmgr.Command to proto format
func convertCommandToProto(cmd pkgmgr.Command) *reliantv1.PackageCommand {
	return &reliantv1.PackageCommand{
		Name:         cmd.Name,
		Description:  cmd.Description,
		Command:      cmd.Command,
		PackageType:  packageTypeFromString(string(cmd.PackageType)),
		Source:       cmd.Source,
		Category:     cmd.Category,
		Dependencies: cmd.Dependencies,
		WorkingDir:   cmd.WorkingDir,
		RelativePath: cmd.RelativePath,
	}
}
