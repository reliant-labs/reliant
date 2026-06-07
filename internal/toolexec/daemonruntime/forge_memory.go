// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"log/slog"
	"os"
	"path/filepath"

	forgecli "github.com/reliant-labs/forge/cli"
)

// projectMemoryWithForgeFramework returns the project's memory bytes
// for snapshot injection. When forge.yaml is present at projectPath, it
// prepends forge's rendered framework memory so every LLM session for a
// forge project gets the framework cheat-sheet (architecture, proto
// rules, critical rules, "use forge skills" callout) without forge
// having to write a top-level reliant.md to disk — that file would
// otherwise drift out of date with framework upgrades.
//
// The user's hand-written reliant.md (when present) is preserved and
// appended after the framework block so user-specific notes have the
// final word. When there is no forge.yaml — or rendering fails for any
// reason — the function returns the on-disk bytes unchanged, leaving
// the legacy behavior intact for non-forge projects.
func projectMemoryWithForgeFramework(projectPath string, onDisk []byte) []byte {
	if projectPath == "" {
		return onDisk
	}
	if _, err := os.Stat(filepath.Join(projectPath, "forge.yaml")); err != nil {
		return onDisk
	}
	framework, err := forgecli.RenderProjectMemory(projectPath)
	if err != nil {
		// Best-effort: a forge.yaml that can't be rendered (parse
		// error, missing name, etc.) shouldn't break the snapshot.
		// Surface the failure to logs so a misconfigured project is
		// debuggable without breaking the session.
		slog.Debug("forge framework memory render failed",
			"projectPath", projectPath, "error", err)
		return onDisk
	}
	if len(onDisk) == 0 {
		return framework
	}
	out := make([]byte, 0, len(framework)+len(onDisk)+2)
	out = append(out, framework...)
	out = append(out, '\n', '\n')
	out = append(out, onDisk...)
	return out
}
