// Copyright (c) 2025 Reliant Labs

// Package gitref holds pure, dependency-free helpers for working with git
// reference names (branch names). It deliberately imports no os/exec/filesystem
// packages so it is safe to call from the server tier (API/worker/gateway),
// which must never touch the filesystem or shell out to git — that boundary is
// enforced by the architecture contract test. Anything that actually runs git
// or reads the working tree lives in internal/gitutil (daemon-side) instead.
package gitref

import (
	"fmt"
	"strings"
)

// NormalizeBranchName trims surrounding whitespace from a user-supplied
// branch name and falls back to "main" when the result is empty. Callers
// should validate the result with ValidateBranchName before handing it to
// git or persisting it.
func NormalizeBranchName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "main"
	}
	return trimmed
}

// ValidateBranchName rejects branch names git itself would refuse
// (a conservative pure-Go mirror of `git check-ref-format --branch`).
// Validating up front lets callers fail with a clear error BEFORE any
// on-disk state (a half-initialized .git) is created.
func ValidateBranchName(name string) error {
	invalid := func(reason string) error {
		return fmt.Errorf("invalid branch name %q: %s", name, reason)
	}
	if name == "" {
		return invalid("must not be empty")
	}
	if name == "@" {
		return invalid("must not be \"@\"")
	}
	if strings.HasPrefix(name, "-") {
		return invalid("must not start with \"-\"")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//") {
		return invalid("must not start or end with \"/\" or contain \"//\"")
	}
	if strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
		return invalid("must not end with \".\" or contain \"..\"")
	}
	if strings.Contains(name, "@{") {
		return invalid("must not contain \"@{\"")
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".") {
			return invalid("path components must not start with \".\"")
		}
		if strings.HasSuffix(component, ".lock") {
			return invalid("path components must not end with \".lock\"")
		}
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7F {
			return invalid("must not contain control characters")
		}
		switch r {
		case ' ', '~', '^', ':', '?', '*', '[', '\\':
			return invalid(fmt.Sprintf("must not contain %q", r))
		}
	}
	return nil
}
