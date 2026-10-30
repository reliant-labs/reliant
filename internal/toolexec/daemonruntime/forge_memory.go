// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/reliant/internal/forgepin"
	"github.com/reliant-labs/reliant/internal/logging"
)

// forgeMemoryWarn limits the render-failure WARN to one per project per hour.
var forgeMemoryWarn = logging.NewThrottle(time.Hour, time.Now)

// pinnedForge runs the forge projects pin; the tests replace it.
var pinnedForge = forgepin.Default

// embeddedForgeIdentity names the forge compiled into this binary, for
// forgeFrameworkMemoryFrom's identity.
const embeddedForgeIdentity = "embedded"

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
	framework, _, ok := forgeFrameworkMemoryFrom(projectPath)
	return framework, ok
}

// forgeFrameworkMemoryFrom is forgeFrameworkMemory plus the identity of the
// forge whose guide it is: one forge's guide is the same for every repo it is
// rendered for, and two forges' are not.
//
// WHICH FORGE. The guide describes the forge that renders it — its commands,
// rules and layout. In a project that pins another forge than the one
// compiled into reliant, it is asked of THAT forge (`forge project memory
// --json`, cached per pinned forge by forgepin); the embedded forge renders
// it only for a project with no pin, or one pinned to the embedded forge.
// When the pinned forge cannot be obtained, or cannot print its guide (a pin
// that predates the command), the block is an explicit note saying so —
// never the embedded forge's guide, which would be the wrong version's rules
// presented as the project's.
func forgeFrameworkMemoryFrom(projectPath string) ([]byte, string, bool) {
	if projectPath == "" {
		return nil, "", false
	}
	if _, err := os.Stat(filepath.Join(projectPath, "forge.yaml")); err != nil {
		return nil, "", false
	}
	pf, pinned, err := pinnedForge.Pinned(projectPath, "project memory")
	if err != nil {
		warnForgeMemory(projectPath, "the project's pinned forge could not be obtained; sessions get a note instead of its framework guide", err)
		return pinnedMemoryUnavailable(err), "unavailable:" + forgepin.Fingerprint([]byte(err.Error())), true
	}
	if pinned {
		memory, err := pinnedProjectMemory(pf, projectPath)
		if err != nil {
			warnForgeMemory(projectPath, "the project's pinned forge did not print its framework guide; sessions get a note instead", err)
			return pinnedMemoryUnavailable(err), "unavailable:" + pf.Key, true
		}
		return memory, pf.Key, true
	}
	framework, err := forgecli.RenderProjectMemory(projectPath)
	if err != nil {
		// Best-effort: a forge.yaml that can't be rendered (parse
		// error, missing name, etc.) shouldn't break the snapshot.
		// WARN, not DEBUG: every LLM session for this project starts without
		// the framework memory, and nothing else says so. Throttled per
		// project, because it is re-rendered for every snapshot.
		warnForgeMemory(projectPath, "forge framework memory render failed; sessions start without it", err)
		return nil, "", false
	}
	return framework, embeddedForgeIdentity, true
}

// pinnedProjectMemory is the guide the pinned forge pf renders for the
// project at root. Besides the forge, the guide depends on forge.yaml (the
// project's name) and on whether the root is a static site (go.mod absent, a
// package.json and a frontend's config at the root), so those join the cache
// key; nothing else in the tree changes it.
func pinnedProjectMemory(pf forgecli.PinnedForge, root string) ([]byte, error) {
	raw, err := pinnedForge.Output(pf, root, "memory-"+memoryFingerprint(root)+".json",
		[]string{"project", "memory", "--json"})
	if err != nil {
		return nil, fmt.Errorf("%s cannot print its framework guide (a forge older than `forge project memory` has no way to): %w", pf.Want, err)
	}
	var doc struct {
		Memory string `json:"memory"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || strings.TrimSpace(doc.Memory) == "" {
		return nil, fmt.Errorf("%s: `project memory --json` printed no guide (%v)", pf.Want, err)
	}
	return []byte(doc.Memory), nil
}

// memoryFingerprint names what, besides the forge, the guide is rendered
// from: forge.yaml, package.json, and the names at the project root.
func memoryFingerprint(root string) string {
	yaml, _ := os.ReadFile(filepath.Join(root, "forge.yaml"))
	pkg, _ := os.ReadFile(filepath.Join(root, "package.json"))
	var names []string
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return forgepin.Fingerprint(yaml, pkg, []byte(strings.Join(names, "\x00")))
}

// pinnedMemoryUnavailable is the framework block when the project's pinned
// forge cannot say what its guide is.
func pinnedMemoryUnavailable(cause error) []byte {
	name := forgepin.CLIName()
	return []byte("# Forge framework guide: unavailable\n\n" +
		"This is a Forge project (Reliant Forge), but the forge it pins in forge.yaml (`forge_version`) " +
		"could not provide its framework guide, so the guide is not shown here. The forge built into " +
		"Reliant is a different version, and its guide would describe commands, rules and layout this " +
		"project does not have — so it is deliberately not used in its place.\n\n" +
		"```\n" + strings.TrimSpace(cause.Error()) + "\n```\n\n" +
		"Until this is fixed, read the conventions from the pinned forge's own skills: `" + name +
		" skill load forge` (in this project, `" + name + "` runs the pinned forge, and fails the same way " +
		"if it cannot be obtained). To fix it: get the pinned forge (offline? a pin nobody published?), " +
		"or move the pin deliberately with `" + name + " project upgrade`.\n")
}

func warnForgeMemory(projectPath, msg string, err error) {
	if suppressed, ok := forgeMemoryWarn.Allow(projectPath); ok {
		slog.Warn(msg, "project_path", projectPath, "error", err, "suppressed", suppressed)
	}
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
