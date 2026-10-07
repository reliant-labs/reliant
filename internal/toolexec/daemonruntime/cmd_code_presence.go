// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func init() {
	RegisterCommand("project.code_presence", handleCodePresence)
}

// --- project.code_presence ---
//
// Answers one question for the API tier: does this directory already contain
// source code? A chat opened on a directory with no code is a greenfield
// request, and the API server injects stack guidance on that basis.
//
// This is deliberately NOT dirIsEffectivelyEmpty (cmd_project.go). That
// predicate gates git auto-init, where a wrong answer runs `git init` inside a
// directory the user did not want touched, so it treats ANY unrecognized entry
// as "not empty". Here a wrong answer costs a paragraph of prompt, so the
// question is the looser and more useful one — "is there code here" rather than
// "which dotfiles are present". Same subject, different risk, different
// function.
//
// The scan also reports the config files it found (.gitignore, .vscode,
// editorconfig...). A directory holding only a .gitignore full of node_modules/
// has no code but is NOT silent about its stack, and the caller passes those
// names to the model so it reads them before recommending anything.

type codePresenceRequest struct {
	Path string `json:"path"`
}

type codePresenceResponse struct {
	// HasCode is true when at least one source file was found.
	HasCode bool `json:"has_code"`
	// CodeFiles names the source file that decided HasCode (a relative
	// path). The scan stops at the first one, so this holds at most one
	// entry. Empty when HasCode is false.
	CodeFiles []string `json:"code_files,omitempty"`
	// ConfigFiles samples non-code files that may still declare a stack —
	// .gitignore, .vscode/*, .editorconfig. Complete (up to the sample
	// limit) when HasCode is false, which is the only case the caller reads
	// it. When HasCode is true it holds only what the scan passed before
	// stopping.
	ConfigFiles []string `json:"config_files,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// codePresenceSampleLimit bounds ConfigFiles. The caller only needs enough
// names to describe the directory to a model.
const codePresenceSampleLimit = 20

// codePresenceScanLimit bounds the walk of a directory that holds no code. A
// directory with thousands of non-code files is emphatically not greenfield,
// and the answer is decided long before a full walk would finish.
const codePresenceScanLimit = 2000

// skippedScanDirs never contain a signal that changes the answer, and are the
// directories most likely to make the walk expensive.
var skippedScanDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".venv":        true,
	"venv":         true,
	"__pycache__":  true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".next":        true,
	".cache":       true,
	".idea":        true,
	".reliant":     true,
}

// nonCodeExtensions are file extensions that carry no stack commitment on
// their own. A directory holding only these is still greenfield: prose, a
// license, and a screenshot describe intent, not an implementation.
var nonCodeExtensions = map[string]bool{
	".md":       true,
	".markdown": true,
	".txt":      true,
	".rst":      true,
	".adoc":     true,
	".pdf":      true,
	".png":      true,
	".jpg":      true,
	".jpeg":     true,
	".gif":      true,
	".svg":      true,
	".webp":     true,
	".ico":      true,
	".log":      true,
}

// nonCodeNames are extensionless (or oddly-extensioned) files that are
// likewise not an implementation.
var nonCodeNames = map[string]bool{
	"license":        true,
	"licence":        true,
	"copying":        true,
	"notice":         true,
	"authors":        true,
	"contributors":   true,
	"readme":         true,
	"changelog":      true,
	".gitignore":     true,
	".gitattributes": true,
	".editorconfig":  true,
	".gitkeep":       true,
	".ds_store":      true,
	"reliant.md":     true,
}

// configFileNames are files that declare tooling or editor preferences without
// being code. They do not make a project non-greenfield, but they can name a
// stack, so they are reported back for the model to read.
var configFileNames = map[string]bool{
	".gitignore":     true,
	".gitattributes": true,
	".editorconfig":  true,
}

func handleCodePresence(ctx context.Context, payload []byte) ([]byte, error) {
	var req codePresenceRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if strings.TrimSpace(req.Path) == "" {
		return json.Marshal(codePresenceResponse{Error: "path is required"})
	}

	result, _, err := scanCodePresence(ctx, req.Path)
	if err != nil {
		return json.Marshal(codePresenceResponse{Error: err.Error()})
	}
	return json.Marshal(result)
}

// codePresenceScanStats reports how much of the tree a scan touched. Tests
// use it to pin the early exit; the handler ignores it.
type codePresenceScanStats struct {
	dirsRead     int
	filesVisited int
}

// scanCodePresence classifies the tree under root, stopping as soon as the
// answer is decided.
//
// The question is "is there ANY code here", so the first code file settles it.
// The walk used to keep going to collect a 20-file sample nobody reads, which
// on a real repo meant up to 2000 files across hundreds of directory reads —
// measured at 8-390ms on a busy laptop (2026-10-05), paid on the StartChat path
// for every first message. Stopping at the first hit makes the occupied case,
// which is nearly every case, a single directory read.
//
// The walk is breadth-first so that hit comes from the shallowest level: a
// project's manifest or entry point sits at its root, while a depth-first walk
// descends through every dotted directory (.claude/, .github/...) that sorts
// ahead of it. Only a directory with NO code is walked to completion, and that
// directory is small by definition — or it reaches codePresenceScanLimit and is
// called occupied anyway.
func scanCodePresence(ctx context.Context, root string) (codePresenceResponse, codePresenceScanStats, error) {
	var (
		resp  codePresenceResponse
		stats codePresenceScanStats
	)

	// Directories to read, as slash-separated paths relative to root.
	pending := []string{""}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return codePresenceResponse{}, stats, fmt.Errorf("scan %s: %w", root, err)
		}
		relDir := pending[0]
		pending = pending[1:]

		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(relDir)))
		stats.dirsRead++
		if err != nil {
			if relDir == "" {
				// The root itself is unreadable or missing. Reporting that as
				// "no code" would inject greenfield guidance on the strength
				// of a directory nobody looked at.
				return codePresenceResponse{}, stats, fmt.Errorf("scan %s: %w", root, err)
			}
			// An unreadable subdirectory is not a reason to fail the whole
			// probe — skip it and keep classifying what we can read.
			continue
		}

		// os.ReadDir sorts by name, so the scan — and the answer it reports —
		// is deterministic for a given tree.
		for _, entry := range entries {
			name := entry.Name()
			rel := path.Join(relDir, name)
			if entry.IsDir() {
				if !skippedScanDirs[strings.ToLower(name)] {
					pending = append(pending, rel)
				}
				continue
			}

			stats.filesVisited++
			if stats.filesVisited > codePresenceScanLimit {
				// Far past any plausible greenfield directory.
				resp.HasCode = true
				return resp, stats, nil
			}

			// Config classification runs FIRST. An editor directory holds
			// real file types — .vscode/settings.json is json — and the code
			// test would otherwise claim them. Editor preferences are not an
			// implementation, so a directory containing only them is still
			// greenfield.
			if isStackDeclaringConfig(rel, name) {
				if len(resp.ConfigFiles) < codePresenceSampleLimit {
					resp.ConfigFiles = append(resp.ConfigFiles, rel)
				}
				continue
			}

			if isCodeFile(name) {
				resp.HasCode = true
				resp.CodeFiles = []string{rel}
				return resp, stats, nil
			}
		}
	}

	// Only reached when there is no code: the full config list is what the
	// caller names in the guidance, so keep it stable for a given directory.
	sort.Strings(resp.ConfigFiles)
	return resp, stats, nil
}

// isCodeFile reports whether a file name represents an implementation — source,
// a manifest, or infrastructure. Anything that is not explicitly recognized as
// prose/config counts as code: the failure that matters is calling an occupied
// directory greenfield, so an unknown extension resolves toward "there is
// something here".
func isCodeFile(name string) bool {
	lower := strings.ToLower(name)
	if nonCodeNames[lower] {
		return false
	}
	ext := strings.ToLower(filepath.Ext(lower))
	if ext != "" && nonCodeExtensions[ext] {
		return false
	}
	// A bare name with no extension and no match above (Makefile, Dockerfile,
	// Procfile) is build tooling — an implementation decision already made.
	return true
}

// isStackDeclaringConfig reports whether a non-code file may still name a
// language, framework or toolchain. A .gitignore listing node_modules/ and a
// .vscode/settings.json pinning a Python interpreter are both stack
// declarations, even though neither is code.
func isStackDeclaringConfig(rel, name string) bool {
	if configFileNames[strings.ToLower(name)] {
		return true
	}
	return strings.HasPrefix(rel, ".vscode/") || strings.HasPrefix(rel, ".idea/")
}
