// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"path"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/filetree"
	"github.com/reliant-labs/reliant/internal/logging"
)

// File preconditions for MCP servers.
//
// A stateless server costs a pipe and a few hundred kilobytes, so starting one
// speculatively is free. A server that INDEXES A TREE is the opposite: it pays
// its entire cost — the walk, the parse, the open descriptors, the resident
// index — before it can answer even one question, including the question "is
// there anything here for me to do?".
//
// Measured, opening a Unity/C# checkout with zero .go files and no go.mod: the
// Go language server reliant spawned for it indexed 9.9 GB and reached 28,467
// open file descriptors and 1.9 GB resident, holding node_modules variants and
// a build .zip. macOS's system-wide file table is 491,520 entries, so roughly
// seventeen such projects exhaust it for EVERY process on the machine. That is
// what happened: Docker's backend, the desktop app and Spotlight all died of
// ENFILE.
//
// Reliant already knew the shape of this fix and applied it to exactly one
// server — detectSystemChrome gates the built-in browser MCP on a Chrome binary
// existing, "so it never spawns an MCP that would immediately fail on
// browser-less images". config.MCPServer.RequiresFiles generalizes it: any
// server can declare the markers that make it worth starting, and reliant
// checks them before spawning.
//
// The check must be CHEAP AND BOUNDED. It exists to prevent an unbounded walk,
// so it cannot itself be one — a precondition that costs what the server costs
// buys nothing. It reuses internal/filetree, the single bounded, gitignore-aware,
// node-budgeted walk this product already has (and which exists because of the
// same Unity checkout), rather than growing a second walker with a second copy
// of "directories we never descend into".
const (
	// preconditionScanDepth bounds how far below the project root the deep pass
	// looks for a marker. Ecosystem markers live at a repository root or one or
	// two levels down inside a monorepo (services/api/go.mod); four levels
	// covers that without turning the check into a survey of the tree.
	preconditionScanDepth = 4

	// preconditionScanNodes is the node budget for one pass, well under
	// filetree.MaxTreeNodes. A marker that a four-thousand-entry scan of a
	// project's own tracked files has not reached is not the thing that makes a
	// heavyweight indexer worth starting.
	preconditionScanNodes = 4000

	// preconditionTTL is how long a verdict is reused before the project is
	// scanned again.
	//
	// A verdict must never be cached permanently: `go mod init` in a project
	// that had no go.mod is a normal thing to do mid-session, and a sticky "no"
	// would mean the server never comes back without a restart. It must also
	// not be recomputed per tool call, because a negative verdict leaves no
	// client in the map to short-circuit on, so every call would rescan.
	//
	// A minute is the balance: at most one bounded scan per (server, project)
	// per minute, and a project that gains its marker starts working within a
	// minute rather than at the next restart. EnsureProjectServersLoaded also
	// drops this project's verdicts outright, so opening or reloading a project
	// always re-decides immediately.
	preconditionTTL = time.Minute
)

// preconditionVerdict is a cached answer plus when it was reached, so it can
// expire. It is keyed by dirClientKey(serverName, projectPath): the question is
// about a (server, project) pair, which is the same pair a dir client is keyed
// by, and that key form is already collision-proof against real server names.
type preconditionVerdict struct {
	met     bool
	decided time.Time
}

// requiredFilePatterns returns cfg's declared markers with blanks dropped.
// A nil/empty result means the server declared no precondition.
func requiredFilePatterns(cfg config.MCPServer) []string {
	if len(cfg.RequiresFiles) == 0 {
		return nil
	}
	out := make([]string, 0, len(cfg.RequiresFiles))
	for _, p := range cfg.RequiresFiles {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, toSlashPattern(trimmed))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// toSlashPattern normalizes a config-authored pattern (and the paths it is
// matched against) to slash separators, so matching behaves identically
// regardless of the host's path separator. Config is written once and may be
// synced between machines.
func toSlashPattern(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// matchesRequirement reports whether a project-relative path satisfies one
// declared pattern.
//
// A pattern containing "/" is anchored: it is matched against the whole
// project-relative path, so "cmd/*.go" means what it says. A pattern without
// one is matched against the BASE NAME at any depth the scan reached, because
// "go.mod" plainly means "this project contains a Go module", not "this project
// is a Go module at its very root" — a monorepo with services/api/go.mod is
// still a project a Go language server should serve.
//
// A malformed pattern matches nothing. It is reported by the caller rather than
// treated as a match: failing open would restore exactly the unbounded spawn
// this field exists to prevent, and a typo that silently disables a server is
// visible in the logs, where a typo that silently re-enables an fd bomb is not.
func matchesRequirement(pattern, relPath string) (bool, error) {
	relPath = toSlashPattern(relPath)
	if strings.Contains(pattern, "/") {
		return path.Match(pattern, relPath)
	}
	return path.Match(pattern, path.Base(relPath))
}

// anyNodeMatches walks a completed scan looking for the first entry satisfying
// any pattern. Directories are matched as well as files: some ecosystem markers
// are directory-shaped (`*.xcodeproj`), and the scan has already excluded the
// dependency/build directories nobody means.
func anyNodeMatches(nodes []*filetree.Node, patterns []string, onBadPattern func(string, error)) bool {
	for _, n := range nodes {
		for _, p := range patterns {
			ok, err := matchesRequirement(p, n.Path)
			if err != nil {
				if onBadPattern != nil {
					onBadPattern(p, err)
				}
				continue
			}
			if ok {
				return true
			}
		}
		if len(n.Children) > 0 && anyNodeMatches(n.Children, patterns, onBadPattern) {
			return true
		}
	}
	return false
}

// scanForRequiredFiles runs one bounded pass and reports whether a marker was
// found and whether the node budget cut the pass short.
func scanForRequiredFiles(patterns []string, projectPath string, depth int, onBadPattern func(string, error)) (found, truncated bool) {
	res, err := filetree.Walk(filetree.Options{
		Root:     projectPath,
		Depth:    depth,
		MaxNodes: preconditionScanNodes,
		// Markers are frequently dot-files (.python-version, .terraform.lock.hcl),
		// and the skip set plus .gitignore — not this flag — are what bound the
		// walk, so including hidden entries costs nothing and prevents a class
		// of surprising misses.
		ShowHidden: true,
	})
	if err != nil {
		// An unreadable project root is not evidence about its contents. Treat
		// it as "no marker found": the alternative is spawning an indexer
		// against a tree reliant cannot even list.
		return false, false
	}
	return anyNodeMatches(res.Nodes, patterns, onBadPattern), res.Truncated
}

// projectHasRequiredFiles reports whether projectPath satisfies patterns.
//
// Two passes, because filetree's walk is DEPTH-first and the budget is finite:
// a single deep pass on a pathological tree can spend its whole budget inside
// the first subdirectory and never reach a marker sitting in plain sight at the
// root. The first pass is therefore the root listing alone — complete, cheap,
// and where the overwhelming majority of markers live. Only if that misses does
// the deep pass run, as a best-effort second chance for monorepos.
//
// truncated reports that the deep pass hit its budget, so a "not found" is
// "not found within the bound" rather than proof of absence. That is still
// treated as NOT SATISFIED, deliberately: the two errors are not symmetric.
// Refusing to start a server the user wanted costs them a tool and says so in
// the log; starting one that indexes a 9.9 GB tree it has no business in costs
// the whole machine its file table. A project that trips this can declare no
// precondition, or a marker the root pass can see.
func projectHasRequiredFiles(patterns []string, projectPath string, onBadPattern func(string, error)) (met, truncated bool) {
	if found, _ := scanForRequiredFiles(patterns, projectPath, 1, onBadPattern); found {
		return true, false
	}
	return scanForRequiredFiles(patterns, projectPath, preconditionScanDepth, onBadPattern)
}

// projectMeetsPrecondition reports whether serverName is worth starting for
// projectPath. It is the single gate: both the eager project autoload
// (EnsureProjectServersLoaded) and the per-project dir client (dirClientFor)
// ask it, so a server can never be spawned through one door after being refused
// at the other.
//
// A server that declares nothing always passes — that is every server that
// exists today, and this must not change any of them.
func (m *Manager) projectMeetsPrecondition(serverName string, cfg config.MCPServer, projectPath string) bool {
	patterns := requiredFilePatterns(cfg)
	if len(patterns) == 0 {
		return true
	}
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		// No tree to check. Unchanged behaviour: the server resolves through the
		// shared client, exactly as it did before preconditions existed.
		return true
	}

	key := dirClientKey(serverName, projectPath)
	now := time.Now()

	m.mu.RLock()
	verdict, cached := m.preconditions[key]
	m.mu.RUnlock()
	if cached && now.Sub(verdict.decided) < preconditionTTL {
		return verdict.met
	}

	met, truncated := projectHasRequiredFiles(patterns, projectPath, func(p string, err error) {
		logging.Warn("Ignoring malformed MCP requiresFiles pattern",
			"server", serverName,
			"pattern", p,
			"error", err)
	})

	m.mu.Lock()
	m.preconditions[key] = preconditionVerdict{met: met, decided: time.Now()}
	m.mu.Unlock()

	if !met {
		logging.Info("Skipping MCP server: project does not contain the files it declared it needs",
			"server", serverName,
			"projectPath", projectPath,
			"requiresFiles", patterns,
			"scanTruncated", truncated,
			"reason", "a tree-indexing server pays its full memory and file-descriptor cost before it can discover it has nothing to do")
	}
	return met
}

// forgetPreconditions drops every cached verdict for a project, so the next ask
// re-scans. Called when a project is (re)loaded: that is the moment the user
// most plausibly just changed what the tree contains, and the moment they would
// expect a newly-initialized module to be picked up without waiting.
func (m *Manager) forgetPreconditions(projectPath string) {
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		return
	}
	suffix := dirKeySeparator + projectPath
	m.mu.Lock()
	for key := range m.preconditions {
		if strings.HasSuffix(key, suffix) {
			delete(m.preconditions, key)
		}
	}
	m.mu.Unlock()
}
