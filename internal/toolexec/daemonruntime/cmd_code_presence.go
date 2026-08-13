// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
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
	// CodeFiles samples the source files found (bounded, relative paths).
	// Empty when HasCode is false.
	CodeFiles []string `json:"code_files,omitempty"`
	// ConfigFiles samples non-code files that may still declare a stack —
	// .gitignore, .vscode/*, .editorconfig, README, LICENSE. Present
	// regardless of HasCode.
	ConfigFiles []string `json:"config_files,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// codePresenceSampleLimit bounds both sample lists. The caller only needs
// enough names to describe the directory to a model; a full listing of a large
// repo would be a waste of a prompt and of the walk.
const codePresenceSampleLimit = 20

// codePresenceScanLimit bounds the walk itself. A directory with tens of
// thousands of files is emphatically not greenfield, and the answer is already
// decided long before the walk would finish.
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

func handleCodePresence(_ context.Context, payload []byte) ([]byte, error) {
	var req codePresenceRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if strings.TrimSpace(req.Path) == "" {
		return json.Marshal(codePresenceResponse{Error: "path is required"})
	}

	result, err := scanCodePresence(req.Path)
	if err != nil {
		return json.Marshal(codePresenceResponse{Error: err.Error()})
	}
	return json.Marshal(result)
}

// scanCodePresence walks path and classifies what it finds. Exported behavior
// is the response; the walk itself is bounded on both file count and sample
// size so an accidental call against a huge tree stays cheap.
func scanCodePresence(root string) (codePresenceResponse, error) {
	var resp codePresenceResponse
	scanned := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory is not a reason to fail the whole
			// probe — skip it and keep classifying what we can read.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}

		name := d.Name()
		if d.IsDir() {
			if skippedScanDirs[strings.ToLower(name)] {
				return fs.SkipDir
			}
			return nil
		}

		scanned++
		if scanned > codePresenceScanLimit {
			// Far past any plausible greenfield directory.
			resp.HasCode = true
			return fs.SkipAll
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = name
		}
		rel = filepath.ToSlash(rel)

		// Config classification runs FIRST. An editor directory holds real
		// file types — .vscode/settings.json is json, .idea/*.xml is xml —
		// and the code test would otherwise claim them. Editor preferences
		// are not an implementation, so a directory containing only them is
		// still greenfield.
		if isStackDeclaringConfig(rel, name) {
			if len(resp.ConfigFiles) < codePresenceSampleLimit {
				resp.ConfigFiles = append(resp.ConfigFiles, rel)
			}
			return nil
		}

		if isCodeFile(name) {
			resp.HasCode = true
			if len(resp.CodeFiles) < codePresenceSampleLimit {
				resp.CodeFiles = append(resp.CodeFiles, rel)
			}
		}
		return nil
	})
	if err != nil {
		return codePresenceResponse{}, fmt.Errorf("scan %s: %w", root, err)
	}

	// Deterministic output: the walk order is filesystem-dependent, and a
	// stable list keeps the injected prompt stable for the same directory.
	sort.Strings(resp.CodeFiles)
	sort.Strings(resp.ConfigFiles)
	return resp, nil
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
