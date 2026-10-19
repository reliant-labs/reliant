// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"strings"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// previewSettings is a server role's PreviewConfig in the shape the tools
// package takes.
func previewSettings(cfg *configv1.PreviewConfig) (tools.PreviewSettings, error) {
	if p := cfg.GetWorkspaceUrlPort(); p < 0 || p > 65535 {
		return tools.PreviewSettings{}, fmt.Errorf("config: WORKSPACE_URL_PORT = %d is not a TCP port", p)
	}
	return tools.PreviewSettings{
		ProxyHost:           strings.TrimSpace(cfg.GetProxyHost()),
		WorkspaceBaseDomain: strings.TrimSpace(cfg.GetWorkspaceBaseDomain()),
		WorkspaceURLScheme:  cfg.GetWorkspaceUrlScheme(),
		WorkspaceURLPort:    int(cfg.GetWorkspaceUrlPort()),
	}, nil
}
