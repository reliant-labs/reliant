// Package statefile is the one place forge's JSON-record-under-.forge/state
// helpers live. Three callers — internal/cli (build_state.go),
// internal/buildtarget (per-service build state), and
// internal/deploytarget (per-provider deploy state) — each used to
// hand-roll the same MkdirAll + MarshalIndent + WriteFile dance and the
// same missing-file-is-nil read, against the same hard-coded
// `.forge/state` directory. They drifted: only deploytarget sanitized
// the env/service path segments before joining them into a filename, so
// buildtarget's statePath was a latent path-traversal smell. Hoisting
// the IO here makes the on-disk dir, the file mode, and the one
// sanitizer a single source of truth.
//
// Scope is deliberately small: this package owns *how* a record is read
// and written (directory, modes, JSON encoding, missing-file semantics),
// NOT *what* the record is or how its filename is composed. Each caller
// keeps its own struct and its own filename builder — the filename shapes
// (build-<env>.json, build-<env>-<service>.json,
// <provider>-<env>-<service>.json) are part of each caller's on-disk
// contract and must stay stable so existing state files keep loading.
package statefile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// DirRel is the per-project on-disk location, relative to the project
// root (the directory holding forge.yaml), for forge runtime state that
// must survive across `forge build` / `forge env deploy` invocations. Sits
// under .forge/ so the single existing `.forge/` .gitignore rule covers
// it alongside checksums.json and the ownership state.
const DirRel = ".forge/state"

// dirMode / fileMode are the modes the three legacy impls all used:
// 0o755 for the lazily-created state dir, 0o644 (world-readable) for the
// file itself — nothing in a state record is secret, and a user peeking
// at the file by hand shouldn't need sudo.
const (
	dirMode  os.FileMode = 0o755
	fileMode os.FileMode = 0o644
)

// SafeSegment sanitizes one path segment (an env or service name) down to
// a flat filename-safe token: [A-Za-z0-9_-] pass through, everything else
// (path separators included) becomes '_'. The inputs are KCL-validated
// identifiers in practice, but a state filename is composed from them, so
// we strip separators defensively to keep the write inside .forge/state.
// An empty input yields "_" so the segment is never empty.
func SafeSegment(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-' || c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "_"
	}
	return string(out)
}

// Path joins the project root, .forge/state, and a caller-built filename
// into the absolute path of a state file. The filename is the caller's
// responsibility (each caller owns its own on-disk filename shape); Path
// just centralizes the directory so the layout lives in one place.
func Path(projectDir, filename string) string {
	return filepath.Join(projectDir, DirRel, filename)
}

// Write marshals v as indented JSON and persists it to the state file at
// path, creating .forge/state lazily so projects that never touch a given
// state path never grow the tree. The 2-space MarshalIndent and 0o644
// mode match what all three legacy impls produced, so on-disk files are
// byte-for-byte unchanged. label names the record in error messages
// (e.g. "build state", "deploy state").
func Write(path, label string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("create %s dir: %w", label, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", label, err)
	}
	if err := atomicfile.WriteFile(path, data, fileMode); err != nil {
		return fmt.Errorf("write %s %s: %w", label, path, err)
	}
	return nil
}

// Read loads and JSON-decodes the state file at path into a freshly
// allocated *T. A missing file returns (nil, nil) — every caller treats
// "no state file" as a non-error "no previous record" signal distinct
// from "file exists but is malformed", which returns (nil, err). label
// names the record in error messages.
func Read[T any](path, label string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s %s: %w", label, path, err)
	}
	var st T
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s %s: %w", label, path, err)
	}
	return &st, nil
}
