// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/reliant/internal/logging"
)

// forgeMemoryWarn limits the render-failure WARN to one per project per hour.
var forgeMemoryWarn = logging.NewThrottle(time.Hour, time.Now)

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
	framework, ok := forgeFrameworkMemory(projectPath)
	if !ok {
		return onDisk
	}
	return prependMemory(framework, onDisk)
}

// forgeFrameworkMemory renders the framework block for a forge project.
// ok is false when projectPath is empty, has no forge.yaml, or rendering
// fails.
func forgeFrameworkMemory(projectPath string) ([]byte, bool) {
	if projectPath == "" {
		return nil, false
	}
	if _, err := os.Stat(filepath.Join(projectPath, "forge.yaml")); err != nil {
		return nil, false
	}
	framework, err := forgecli.RenderProjectMemory(projectPath)
	if err != nil {
		// Best-effort: a forge.yaml that can't be rendered (parse
		// error, missing name, etc.) shouldn't break the snapshot.
		// WARN, not DEBUG: every LLM session for this project starts without
		// the framework memory, and nothing else says so. Throttled per
		// project, because it is re-rendered for every snapshot.
		if suppressed, ok := forgeMemoryWarn.Allow(projectPath); ok {
			slog.Warn("forge framework memory render failed; sessions start without it",
				"project_path", projectPath, "error", err, "suppressed", suppressed)
		}
		return nil, false
	}
	return framework, true
}

func prependMemory(head, rest []byte) []byte {
	if len(rest) == 0 {
		return head
	}
	out := make([]byte, 0, len(head)+len(rest)+2)
	out = append(out, head...)
	out = append(out, '\n', '\n')
	out = append(out, rest...)
	return out
}

func hasForgeYAML(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "forge.yaml"))
	return err == nil
}
