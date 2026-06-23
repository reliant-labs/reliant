// Copyright (c) 2025 Reliant Labs
package runtime

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ToolLocationChecker determines if a tool name runs on the daemon.
// Provided by callers to avoid import cycles with the tools package.
type ToolLocationChecker func(toolName string) bool

// ToolFilterExpander expands a tool filter (tags/globs) into concrete tool names.
// Provided by callers to avoid import cycles with the tools package.
type ToolFilterExpander func(filter []string) []string

// PreflightConfig holds functions needed for RequiresDaemon analysis.
// Injected to avoid import cycles between runtime and tools packages.
type PreflightConfig struct {
	// IsDaemonTool returns true if the named tool runs on the daemon.
	IsDaemonTool ToolLocationChecker
	// ExpandToolFilter expands tool filter specs (tags, globs) into tool names.
	ExpandToolFilter ToolFilterExpander
}

// defaultPreflightConfig is the package-level PreflightConfig set by SetPreflightConfig.
// nil means RequiresDaemon will conservatively assume daemon needed for tool filters.
var defaultPreflightConfig *PreflightConfig

// SetPreflightConfig sets the default PreflightConfig used by buildPreflightConfig.
// Called during initialization by the activities package which can import both
// the tools and runtime packages.
func SetPreflightConfig(cfg *PreflightConfig) {
	defaultPreflightConfig = cfg
}

// buildPreflightConfig returns the default PreflightConfig.
// Returns nil if not set (RequiresDaemon will be conservative).
func buildPreflightConfig() *PreflightConfig {
	return defaultPreflightConfig
}

// RequiresDaemon performs static analysis on a workflow definition to determine
// whether any node requires a daemon for execution. Returns true if the workflow
// contains:
//   - Any "run" type node (shell execution always runs on daemon)
//   - Any node with an explicit "daemon" field set
//   - Any call_llm node whose tool_filter includes tools annotated ToolRunsOnDaemon
//   - Workflow-level daemon field set
//
// This is used for preflight checks to fail fast before starting execution
// if a daemon is required but not available.
func RequiresDaemon(wf *reliantv1.Workflow, cfg *PreflightConfig) bool {
	if wf == nil {
		return false
	}

	// Workflow-level daemon field means the workflow explicitly targets a daemon.
	if wf.Daemon != nil {
		return true
	}

	return requiresDaemonNodes(wf.GetNodes(), cfg)
}

// requiresDaemonNodes checks a slice of nodes for daemon requirements.
func requiresDaemonNodes(nodes []*reliantv1.Node, cfg *PreflightConfig) bool {
	for _, node := range nodes {
		if requiresDaemonNode(node, cfg) {
			return true
		}
	}
	return false
}

// requiresDaemonNode checks a single node for daemon requirements.
func requiresDaemonNode(node *reliantv1.Node, cfg *PreflightConfig) bool {
	if node == nil {
		return false
	}

	nodeType := node.GetType()

	// Run nodes always execute on daemon (shell commands).
	if nodeType == model.NodeTypeRun {
		return true
	}

	// Explicit daemon field means user expects a daemon.
	if node.GetDaemon() != nil {
		return true
	}

	// Check call_llm nodes for daemon-bound tools in tool_filter.
	if nodeType == model.NodeTypeCallLLM {
		if args := node.GetCallLlm(); args != nil {
			if toolFilterHasDaemonTools(args.GetToolsConfig().GetFilter(), cfg) {
				return true
			}
		}
	}

	// Recurse into inline workflow/loop definitions.
	if nodeType == model.NodeTypeWorkflow {
		if args := node.GetWorkflow(); args != nil {
			if inline := args.GetInline(); inline != nil {
				if RequiresDaemon(inline, cfg) {
					return true
				}
			}
		}
	}
	if nodeType == model.NodeTypeLoop {
		if args := node.GetLoop(); args != nil {
			if inline := args.GetInline(); inline != nil {
				if RequiresDaemon(inline, cfg) {
					return true
				}
			}
		}
	}

	return false
}

// toolFilterHasDaemonTools checks if a CelStringList tool filter contains any
// tools that run on the daemon. For CEL expressions we can't statically resolve,
// so we conservatively return true (better to check and find a daemon than skip).
func toolFilterHasDaemonTools(tf *reliantv1.CelStringList, cfg *PreflightConfig) bool {
	if tf == nil {
		return false
	}

	// CEL expression — can't evaluate statically, assume daemon may be needed.
	if tf.GetExpr() != "" {
		return true
	}

	lit := tf.GetLiteral()
	if lit == nil {
		return false
	}

	if cfg == nil || cfg.ExpandToolFilter == nil || cfg.IsDaemonTool == nil {
		// No tool analysis available — conservatively assume daemon needed.
		return true
	}

	// Expand the tool filter to get concrete tool names, then check each.
	expanded := cfg.ExpandToolFilter(lit.GetValues())
	for _, name := range expanded {
		if cfg.IsDaemonTool(name) {
			return true
		}
	}
	return false
}
