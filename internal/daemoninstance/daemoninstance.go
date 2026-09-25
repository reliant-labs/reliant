// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: pure naming: projects a daemon instance name onto a stable ~/.reliant directory
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib and the OS, with no collaborator to fake and no second
// implementation. An interface here would have exactly one implementor and one
// caller shape, which is indirection without a seam.

// Package daemoninstance names a daemon instance explicitly and projects that
// name onto a stable directory under ~/.reliant.
//
// # Why this exists
//
// The daemon's data directory used to be `./data` — relative to whatever
// directory the process happened to start in. Two daemons launched from
// different directories inside the SAME repo therefore kept two different
// runtime records and never saw each other, while `reliant daemon stop` run
// from the wrong directory read a record that did not exist, printed "No daemon
// running", and exited zero. On one developer machine this produced live
// daemons under both `reliant/data` and `reliant/electron/data` at once.
//
// The fix is to stop deriving identity from the current working directory and
// state it instead:
//
//		instance = (server origin, account sub, workspace)
//
//	  - origin    which backend this daemon talks to, collapsed to
//	    scheme://host:port. Separates dev, staging and prod.
//	  - sub       the Supabase subject the credentials belong to. Separates
//	    accounts on a shared machine. May be empty — that is the
//	    not-yet-signed-in and self-hosted case, and it maps to the
//	    explicit "_default" segment rather than to nothing.
//	  - workspace the worktree path, or an explicit label. Separates the many
//	    concurrent worktrees this project is developed in.
//
// projected to:
//
//	~/.reliant/instances/<origin>/<sub>/<workspace>/
//
// # Origin collapsing is a parity contract
//
// The origin segment is derived with exactly the semantics of `endpointKey` in
// internal/auth/daemon_file.go, which keys the daemon credentials store, and of
// its hand-mirrored twin in electron/src/daemon-creds.js. If the three ever
// disagree, a daemon writes its runtime record under one instance and reads its
// credentials from another — split-brain that looks like "my PAT vanished".
//
// The logic is duplicated here rather than imported because the import must not
// point this way: internal/auth is a large package (middleware, OAuth, PATs)
// that twenty-odd packages depend on, and it is the credentials store that will
// want to reference an instance key, not the reverse. Duplication plus a pinned
// parity test (internal/auth/daemoninstance_parity_test.go) keeps the edge free
// without letting the two implementations drift.
//
// # Unparseable input is refused, never defaulted
//
// Like endpointKey, a server URL that does not parse yields no key. Resolve
// returns ErrInvalidOrigin rather than substituting a default, because the one
// thing worse than refusing to start is starting against silently the wrong
// backend.
package daemoninstance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/builddefaults"
	"github.com/reliant-labs/reliant/internal/instanceid"
)

const (
	// InstancesDirName is the container for every instance directory, directly
	// under ~/.reliant.
	InstancesDirName = "instances"

	// DefaultSubSegment stands in for an absent account. It is a real, explicit
	// path segment: an empty one would collapse the path and silently merge the
	// signed-out instance into its parent.
	DefaultSubSegment = "_default"

	// EnvWorkspace pins the workspace component explicitly. Intended for
	// containers and CI, where the working directory is not a meaningful
	// workspace identity but the workload's is.
	EnvWorkspace = "RELIANT_INSTANCE_WORKSPACE"

	// EnvServerURL is the same server-URL override the rest of the binary
	// honours, consulted when no origin is passed.
	EnvServerURL = "RELIANT_SERVER_URL"

	// slugHumanMax bounds the readable prefix of a slug segment. Long enough to
	// recognize a host or a worktree at a glance, short enough that
	// `ls ~/.reliant/instances/*/*` stays readable in a terminal.
	slugHumanMax = 32

	// slugHashLen is the hex width of the disambiguating suffix. 32 bits over a
	// per-machine population of instances — tens, not millions — leaves
	// collision probability negligible while keeping the segment short.
	slugHashLen = 8
)

// ErrInvalidOrigin reports a server URL that cannot be collapsed to an origin.
// Callers must treat it as "refuse to write", matching endpointKey's contract
// of returning an empty key for unparseable input.
var ErrInvalidOrigin = errors.New("daemoninstance: server URL has no parseable origin")

// Key is the fully-resolved identity of one daemon instance.
//
// Fields are canonical, not raw: Origin has been collapsed to scheme://host:port
// and lowercased, Workspace has been made absolute, and Sub has been trimmed.
// Construct one with Resolve; a zero Key names nothing.
type Key struct {
	Origin    string
	Sub       string
	Workspace string
}

// Resolve canonicalizes the three components and applies defaults.
//
// An empty origin falls back to the binary's configured server URL (the
// RELIANT_SERVER_URL override, then the compiled-in default), so a caller that
// has no explicit flag lands on the same backend the rest of the process talks
// to. An origin that is present but unparseable is an error.
//
// An empty workspace falls back to EnvWorkspace, then to the current working
// directory. A workspace that looks like a filesystem path is made absolute and
// symlink-resolved so that two spellings of one directory are one instance;
// anything else is kept verbatim as a label.
func Resolve(origin, sub, workspace string) (Key, error) {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		origin = builddefaults.Value(EnvServerURL, builddefaults.ServerURL, builddefaults.NeutralServerURL)
	}

	canonicalOrigin := OriginKey(origin)
	if canonicalOrigin == "" {
		return Key{}, fmt.Errorf("%w: %q", ErrInvalidOrigin, origin)
	}

	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = strings.TrimSpace(os.Getenv(EnvWorkspace))
	}
	if workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Key{}, fmt.Errorf("daemoninstance: resolving working directory as workspace: %w", err)
		}
		workspace = cwd
	}

	return Key{
		Origin:    canonicalOrigin,
		Sub:       strings.TrimSpace(sub),
		Workspace: canonicalWorkspace(workspace),
	}, nil
}

// OriginKey collapses a server URL to scheme://host:port.
//
// Path, query and fragment are dropped, so `https://staging.reliantapi.com/grpc`
// and `https://staging.reliantapi.com/api` name one instance, while
// `http://localhost:8090` and `http://localhost:8690` name two. A URL with no
// explicit port keeps its host unchanged — the scheme's default port is
// implicit, and inventing it here would disagree with the credentials store.
//
// Returns "" for unparseable input; callers must refuse rather than default.
// This MUST stay behaviourally identical to endpointKey in
// internal/auth/daemon_file.go and to its mirror in electron/src/daemon-creds.js.
func OriginKey(serverURL string) string {
	s := strings.TrimSpace(serverURL)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + strings.ToLower(u.Host)
}

// OriginSlug is the origin as one filesystem-safe, readable path segment,
// e.g. "http-localhost-8090-1d4f2c90".
func (k Key) OriginSlug() string { return slugSegment(k.Origin, k.Origin) }

// SubSlug is the account as one path segment. An empty Sub — not signed in, or
// self-hosted with no account at all — is DefaultSubSegment exactly, so the
// path never gains an empty component.
func (k Key) SubSlug() string {
	if k.Sub == "" {
		return DefaultSubSegment
	}
	return slugSegment(k.Sub, k.Sub)
}

// WorkspaceSlug is the workspace as one path segment. For a path, the readable
// prefix is the directory's base name and the hash covers the WHOLE path, so
// two worktrees that share a base name still get distinct segments.
func (k Key) WorkspaceSlug() string {
	human := k.Workspace
	if looksLikePath(k.Workspace) {
		human = filepath.Base(k.Workspace)
		if human == string(filepath.Separator) || human == "." || human == ".." {
			human = "root"
		}
	}
	return slugSegment(human, k.Workspace)
}

// Slug is the instance's relative location: "<origin>/<sub>/<workspace>",
// always three non-empty segments, always separated by "/" regardless of OS so
// it is stable as a log field and a map key. Join it onto InstancesDir to get a
// filesystem path.
func (k Key) Slug() string {
	return k.OriginSlug() + "/" + k.SubSlug() + "/" + k.WorkspaceSlug()
}

// DataDir is the absolute directory this instance owns:
// ~/.reliant/instances/<origin>/<sub>/<workspace>. It does not create anything;
// see EnsureDataDir.
func (k Key) DataDir() (string, error) {
	root, err := InstancesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, k.OriginSlug(), k.SubSlug(), k.WorkspaceSlug()), nil
}

// EnsureDataDir creates the instance directory if it is absent and returns it.
// 0700 throughout: an instance directory holds runtime records and, for
// consumers that put them there, credentials.
func (k Key) EnsureDataDir() (string, error) {
	dir, err := k.DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("daemoninstance: creating instance data dir %s: %w", dir, err)
	}
	return dir, nil
}

// String renders the key for logs and error messages, in the composite form a
// human reads rather than the slugged form a filesystem needs.
func (k Key) String() string {
	sub := k.Sub
	if sub == "" {
		sub = DefaultSubSegment
	}
	return k.Origin + " " + sub + " " + k.Workspace
}

// InstancesDir is ~/.reliant/instances, the parent of every instance directory.
func InstancesDir() (string, error) {
	dir, err := instanceid.Dir()
	if err != nil {
		return "", fmt.Errorf("daemoninstance: resolving reliant state dir: %w", err)
	}
	return filepath.Join(dir, InstancesDirName), nil
}

// canonicalWorkspace makes a path workspace absolute and symlink-free so the
// several spellings of one directory — a relative path, a trailing slash, a
// symlinked /var on macOS — resolve to a single instance. A non-path label is
// returned untouched.
//
// Symlink resolution is best-effort: a workspace that does not exist yet is
// still a legitimate instance name, and failing here would refuse to start over
// a directory the caller is about to create.
func canonicalWorkspace(workspace string) string {
	if !looksLikePath(workspace) {
		return workspace
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return filepath.Clean(workspace)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// looksLikePath distinguishes a workspace given as a directory from one given
// as a label. Anything containing a separator, or beginning with "." or "~", is
// a path; a bare word like "ci" or "release" is a label.
func looksLikePath(workspace string) bool {
	if workspace == "" {
		return false
	}
	if filepath.IsAbs(workspace) ||
		strings.HasPrefix(workspace, ".") ||
		strings.HasPrefix(workspace, "~") {
		return true
	}
	return strings.ContainsRune(workspace, '/') || strings.ContainsRune(workspace, filepath.Separator)
}

// slugSegment renders one path segment as "<readable>-<hash>".
//
// Both halves are load-bearing and neither alone is sufficient. The readable
// half exists so `ls ~/.reliant/instances` tells a human something; it is lossy
// by construction — it strips `://`, collapses runs of unsafe characters, and
// truncates — so on its own it collides. The hash is taken over the FULL
// canonical value, never over the truncated prefix, so inputs that differ only
// in a dropped or trimmed character ("…:8090" vs "…:8690") still differ here.
func slugSegment(human, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return sanitize(human) + "-" + hex.EncodeToString(sum[:])[:slugHashLen]
}

// sanitize reduces a string to lowercase [a-z0-9._-], collapsing every run of
// anything else to a single "-", and truncates it to slugHumanMax.
//
// "." and "-" survive because they keep hostnames and worktree names legible.
// The result is never empty and never "." or "..": an input with nothing
// readable in it degrades to "x" and leans entirely on the hash suffix, which
// is correct rather than merely defensive.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
		default:
			pendingDash = b.Len() > 0
		}
		if b.Len() >= slugHumanMax {
			break
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "x"
	}
	if len(out) > slugHumanMax {
		out = strings.Trim(out[:slugHumanMax], "-.")
	}
	return out
}
