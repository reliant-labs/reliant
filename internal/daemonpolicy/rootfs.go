// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveFile returns a path a handler may safely operate on, given the policy
// in ctx.
//
// For an unconfined caller it returns the path unchanged, so first-party
// behavior is untouched. For a confined caller it verifies the path through
// the kernel rather than through string comparison: os.Root resolves each
// component with the root as an escape-proof boundary, so a symlink swapped in
// after an earlier check still cannot escape.
//
// This is the durable half of path confinement. The payload-walking check in
// paths.go rejects an obviously-out-of-bounds request before the daemon does
// any work, but it infers intent from argument NAMES and therefore cannot
// cover a command whose payload it does not recognize. This function is called
// by the handler that is about to touch the file, so it covers the operation
// that actually happens.
func ResolveFile(ctx context.Context, path string) (string, error) {
	p := FromContext(ctx)
	if p == nil {
		return path, nil
	}
	return p.resolveWithin(path, false)
}

// ResolveDir is ResolveFile for a path that must be a directory, or that a
// handler is about to create.
func ResolveDir(ctx context.Context, path string) (string, error) {
	p := FromContext(ctx)
	if p == nil {
		return path, nil
	}
	return p.resolveWithin(path, true)
}

// RootFor returns an *os.Root scoped to the policy's path root, for handlers
// that want to perform several operations through a kernel-enforced boundary.
//
// It returns (nil, nil) for an unconfined caller — the caller then uses the
// ordinary os package, as before.
func RootFor(ctx context.Context) (*os.Root, error) {
	p := FromContext(ctx)
	if p == nil {
		return nil, nil
	}
	root, err := p.resolvedRoot()
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("%w: the allowed directory is not accessible", ErrDenied)
	}
	return r, nil
}

// resolveWithin verifies path against the policy root using os.Root, and
// returns the absolute path the handler should use.
//
// allowMissing covers creates: the target may not exist yet, so the check
// applies to its parent, which is where an escaping symlink would have to be.
func (p *Policy) resolveWithin(path string, allowMissing bool) (string, error) {
	root, err := p.resolvedRoot()
	if err != nil {
		return "", err
	}

	if strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("%w: path contains an invalid character", ErrDenied)
	}

	// An empty path means the root itself. Handlers otherwise substitute the
	// daemon's working directory, which is not inside the grant.
	if strings.TrimSpace(path) == "" {
		return root, nil
	}

	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)

	// Resolve the candidate into the same namespace as the root before
	// comparing. Both must be resolved or neither: on macOS a temp path is
	// handed out as /var/... while /var is a symlink to /private/var, so
	// comparing a resolved root against an unresolved candidate rejects paths
	// that are genuinely inside it.
	resolvedAbs, err := resolveExisting(abs)
	if err != nil {
		return "", fmt.Errorf("%w: path %q could not be resolved", ErrDenied, path)
	}

	// os.Root takes paths relative to the root, so a path that is not
	// lexically beneath it is rejected before the kernel is involved. This is
	// a fast reject, not the boundary — Rel would happily produce "../x".
	rel, err := filepath.Rel(root, resolvedAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path %q is outside this connector's allowed directory", ErrDenied, path)
	}
	if rel == "." {
		return root, nil
	}

	r, err := os.OpenRoot(root)
	if err != nil {
		return "", fmt.Errorf("%w: the allowed directory is not accessible", ErrDenied)
	}
	defer func() { _ = r.Close() }()

	// Every intermediate component must resolve inside the root. os.Root
	// enforces that in the kernel, so a symlink anywhere in the parent chain
	// that points outside fails here.
	//
	// The parent is checked unconditionally, including for a path that does
	// not exist yet: a create is exactly the case where an escaping parent
	// matters, since the handler is about to bring a new file into existence
	// through it.
	if parent := filepath.Dir(rel); parent != "." {
		if _, perr := r.Lstat(parent); perr != nil && !os.IsNotExist(perr) {
			return "", fmt.Errorf("%w: path %q is outside this connector's allowed directory", ErrDenied, path)
		}
	}

	// The FINAL component must not itself be a symlink.
	//
	// Lstat deliberately does not follow it, so a link — including a dangling
	// one, whose target may not exist yet — returns success here while the
	// handler's own os.WriteFile/os.ReadFile would follow it straight out of
	// the root. Rejecting the link outright is the only answer that holds:
	// this function returns a path string, and any resolution it performs can
	// be undone by the handler re-traversing it.
	info, err := r.Lstat(rel)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf(
				"%w: path %q is a symbolic link, which is not allowed for this connector", ErrDenied, path)
		}
	case os.IsNotExist(err):
		// A missing target is the handler's error to report, not a policy
		// denial — otherwise every create would look like a permission
		// problem. Its parent was verified above.
	default:
		return "", fmt.Errorf("%w: path %q is outside this connector's allowed directory", ErrDenied, path)
	}

	// Return the path rebuilt from the RESOLVED root, so the handler cannot
	// re-traverse a component this function already resolved.
	return filepath.Join(root, rel), nil
}
