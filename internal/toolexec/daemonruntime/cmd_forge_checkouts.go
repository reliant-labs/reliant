// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// =============================================================================
// THE CHECKOUT ALLOWLIST FOR EVERY project_path THE DEPLOY FAMILY ACCEPTS.
//
// `forge project checkouts --json` lists `origin/<main>` plus every git
// worktree of this project. A deploy request may name a checkout to build from;
// accepted unchecked, that field points forge — which builds images, pushes
// them and reads secrets — at an arbitrary directory on the daemon's
// filesystem. So a path is accepted ONLY if forge listed it for this project,
// and the check happens HERE, in the daemon, never in the browser: the daemon
// re-derives the allowlist from git on every call and compares against that,
// so the guarantee does not depend on what any caller sends.
// =============================================================================

// forgeCheckoutsArgs is the argv that lists this project's checkouts.
var forgeCheckoutsArgs = []string{"project", "checkouts", "--json"}

// --- the checkout allowlist --------------------------------------------------

// forgeCheckoutsDoc is the MINIMUM this package needs from forge's document:
// the paths, so a requested checkout can be checked against them.
//
// The same narrow exception to the transport rule the deploy guard takes. No
// verdict is re-derived; these are identity fields, and forge's document
// reaches the caller untouched.
type forgeCheckoutsDoc struct {
	Checkouts []struct {
		Path string `json:"path"`
		Tree string `json:"tree"`
	} `json:"checkouts"`
}

// resolveCheckoutPath returns the checkout to run against, having proved it is
// one forge listed for this project.
//
// An EMPTY requested path is the normal case and means "the project's main
// checkout" — it resolves to projectPath without consulting git, because that
// is the root the caller already named and the one every other forge.* command
// uses.
//
// A NON-EMPTY path is enumerated and compared. The comparison is on the
// CLEANED, SYMLINK-RESOLVED absolute path on both sides, because a path that
// differs only by a trailing slash, a `..` segment or a symlink is the same
// directory and refusing it would be a false negative — while comparing the raw
// strings would let `/allowed/../../etc` pass a prefix test.
//
// A path that is not in the list is refused with a message that does not echo
// the daemon's filesystem layout back: the caller gets "not one of this
// project's checkouts", not a listing of what does exist.
func resolveCheckoutPath(ctx context.Context, projectPath, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return projectPath, nil
	}

	raw, err := invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: projectPath,
		Args:        forgeCheckoutsArgs,
	})
	if err != nil {
		return "", err
	}

	var resp forgeReportResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("%s: could not read this project's checkouts: %v", forgeCommandFailedPrefix, err)
	}
	if !resp.IsForgeProject || !resp.Supported {
		return "", fmt.Errorf("this project's checkouts could not be listed, so a specific checkout " +
			"cannot be authorised; preview the project's main checkout instead")
	}

	var doc forgeCheckoutsDoc
	if err := json.Unmarshal(resp.Report, &doc); err != nil {
		return "", fmt.Errorf("%s: checkout list was not the expected shape: %v", forgeCommandFailedPrefix, err)
	}

	want := canonicalPath(requested)
	for _, checkout := range doc.Checkouts {
		if checkout.Path == "" {
			continue
		}
		if canonicalPath(checkout.Path) == want {
			// Return forge's own spelling rather than the caller's.
			return checkout.Path, nil
		}
	}

	return "", fmt.Errorf("the requested checkout is not one of this project's checkouts, so it will " +
		"not be used: a preview may only render a checkout this project actually has")
}

// canonicalPath is the comparable form of a filesystem path: absolute, cleaned,
// and with symlinks resolved where they can be.
//
// filepath.EvalSymlinks fails on a path that does not exist, and a worktree
// that was removed between the listing and the comparison is a legitimate way
// to reach that. Falling back to the merely-cleaned absolute path keeps the
// comparison total; it cannot create a false ACCEPT, because both sides get the
// same treatment and a non-existent path matches only an identical spelling.
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
