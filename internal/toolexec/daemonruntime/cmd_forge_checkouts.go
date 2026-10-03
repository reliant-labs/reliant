// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func init() {
	RegisterCommand("forge.checkouts", handleForgeCheckouts)
	RegisterCommand("forge.env_diff", handleForgeEnvDiff)
}

// =============================================================================
// forge.checkouts — WHICH CHECKOUTS MAY BE PREVIEWED, AND THEREFORE THE
// ALLOWLIST FOR EVERY project_path THIS FAMILY ACCEPTS.
//
// `forge project checkouts --json` lists `origin/<main>` plus every git
// worktree of this project, each with its branch, HEAD, dirty flag and distance
// from main. Preview renders an arbitrary checkout and diffs it against Live,
// so something has to offer the choice; this is what the picker reads.
//
// IT IS ALSO A SECURITY BOUNDARY, AND THAT IS THE HALF WORTH SPELLING OUT.
//
// A Preview request carries a project_path. Accepted unchecked, that field
// points forge — which builds images, pushes them and reads secrets — at an
// arbitrary directory on the daemon's filesystem. So a path is accepted ONLY if
// this command returned it for this project, and the check happens HERE, in the
// daemon, never in the browser.
//
// Why not in the browser: the browser's list is a hint it fetched, and a
// request is not obliged to resemble the UI that was supposed to produce it.
// A check that lives in the client is a check an attacker simply does not run.
// The daemon re-derives the allowlist from git on every call and compares
// against that, so the guarantee does not depend on what any caller sends.
// =============================================================================

type forgeCheckoutsRequest struct {
	// ProjectPath is the project's MAIN checkout — the root this command
	// enumerates worktrees of.
	ProjectPath string `json:"project_path"`

	// WithTree asks forge for each checkout's tree hash. It costs a
	// `git write-tree` per checkout, so it is opt-in: the env-diff cache is
	// keyed by it, the picker does not need it.
	WithTree bool `json:"with_tree,omitempty"`
}

func (r forgeCheckoutsRequest) validate() error {
	if strings.TrimSpace(r.ProjectPath) == "" {
		return fmt.Errorf("project_path is required")
	}
	return nil
}

func (r forgeCheckoutsRequest) args() []string {
	args := []string{"project", "checkouts", "--json"}
	if r.WithTree {
		args = append(args, "--tree")
	}
	return args
}

// handleForgeCheckouts lists this project's checkouts. Read-only.
func handleForgeCheckouts(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeCheckoutsRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := req.validate(); err != nil {
		return nil, err
	}

	return invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: req.ProjectPath,
		Args:        req.args(),
	})
}

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
		Args:        forgeCheckoutsRequest{ProjectPath: projectPath}.args(),
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

// =============================================================================
// forge.env_diff — §8.2's per-env diff cards.
//
// `forge env diff --json` renders a checkout and diffs it against each
// environment's Live shape. The document reaches the caller VERBATIM and the UI
// renders it as-is: the diff is forge's to compute, and a second
// implementation in reliant would be a copy that disagrees. Nothing here
// interprets a status.
//
// It is READ-ONLY with respect to every cluster — it renders and compares, it
// does not build, push, cut or apply — so it needs no approval token. It is
// still a REAL render, which costs seconds per environment, which is why the
// caller chooses which environments to ask about rather than always paying for
// all of them.
// =============================================================================

type forgeEnvDiffRequest struct {
	ProjectPath string `json:"project_path"`

	// CheckoutPath is the checkout to render. Empty means the project's
	// main checkout. A non-empty value is authorised against
	// `forge project checkouts` before forge is pointed at it — see
	// resolveCheckoutPath.
	CheckoutPath string `json:"checkout_path,omitempty"`

	// Env is the ONE environment to diff. forge's positional argument.
	//
	// Exactly one of Env and All, which is forge's own rule ("name an
	// environment or pass --all (not both, and not neither)"). Mirrored
	// here rather than left to forge so a malformed request fails before a
	// subprocess starts, and with a message about the request rather than
	// about a command line the caller never wrote.
	Env string `json:"env,omitempty"`

	// All diffs every environment declared in this checkout, in one
	// document. This is the form the §8.2 cards use: the UI shows a card
	// per environment, and asking once is cheaper than asking per env
	// because a single render serves them all.
	All bool `json:"all,omitempty"`
}

func (r forgeEnvDiffRequest) validate() error {
	if strings.TrimSpace(r.ProjectPath) == "" {
		return fmt.Errorf("project_path is required")
	}
	env := strings.TrimSpace(r.Env)
	switch {
	case r.All && env != "":
		return fmt.Errorf("env %q and all are contradictory: diff one environment or every one, "+
			"not both", r.Env)
	case !r.All && env == "":
		return fmt.Errorf("env is required (or set all to diff every environment in this checkout)")
	}
	// The env lands in argv as a bare positional, so a leading dash would
	// be consumed by forge as a flag.
	if strings.HasPrefix(env, "-") {
		return fmt.Errorf("env must not begin with '-' (it would be read as a flag): %q", r.Env)
	}
	return nil
}

func (r forgeEnvDiffRequest) args() []string {
	args := []string{"env", "diff", "--json"}
	if r.All {
		return append(args, "--all")
	}
	return append(args, strings.TrimSpace(r.Env))
}

// handleForgeEnvDiff diffs a checkout against Live, per environment.
func handleForgeEnvDiff(ctx context.Context, payload []byte) ([]byte, error) {
	var req forgeEnvDiffRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := req.validate(); err != nil {
		return nil, err
	}

	// The checkout is authorised BEFORE forge is pointed at it.
	checkout, err := resolveCheckoutPath(ctx, req.ProjectPath, req.CheckoutPath)
	if err != nil {
		return nil, err
	}

	return invokeForgeReport(ctx, forgeInvocation{
		ProjectPath: checkout,
		Args:        req.args(),
	})
}
