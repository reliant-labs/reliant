// Package devstack owns forge's parallel-dev-stack primitives: the raw git
// facts that distinguish one working tree from another, and a memoized
// port-block allocator. Together they let N dev stacks (one per git
// worktree) run in parallel against shared clusters without colliding —
// DECLARATIVELY: forge supplies the facts + the allocator, KCL composes
// them.
//
// There is deliberately NO "instance" abstraction and NO user-visible
// index. forge exposes two raw git facts as KCL options and lets the KCL
// author decide which to key on:
//
//	option("worktree") -> the LINKED-worktree directory basename, or "" on
//	                      the PRIMARY checkout (any branch).
//	option("branch")   -> the current git branch, sanitized DNS-safe, always.
//
// and one resolved builtin that hides the port arithmetic entirely:
//
//	forge.allocate_port(base, key) -> base + block(key)*100
//
// where block(key) is a small int forge assigns the first time it sees key
// and persists (see blocks.go). The default — primary checkout, key "" —
// renders byte-identically to a stack with no dev-stack parameterization.
package devstack

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// maxNameLen bounds a sanitized GIT FACT — a worktree or branch name. These
// values feed k8s namespace suffixes, DB names, and NATS subject prefixes —
// all of which have length ceilings (a k8s namespace is 63 chars and already
// carries a project+env prefix), so we keep the fact segment short.
//
// The bound belongs to the FACT, applied once where forge derives it
// (Worktree, Branch), and nowhere else. It is deliberately NOT re-applied to
// a port-block key composed FROM a fact — see validateKey in blocks.go for
// why doing so made a 21-character worktree unable to render prod at all.
const maxNameLen = 24

var (
	nonDNS      = regexp.MustCompile(`[^a-z0-9-]+`)
	dashRuns    = regexp.MustCompile(`-+`)
	leadTrailDA = regexp.MustCompile(`^-+|-+$`)
)

// canonicalLabel reduces s to a DNS-safe label — [a-z0-9-], collapsed dash
// runs, no leading/trailing dash — and applies NO length bound. Returns ""
// when nothing survives (e.g. an all-symbol branch name).
//
// Shape and length are separated because they are answers to different
// questions, and conflating them was a real defect. A caller asking "is this
// string a well-formed name?" must not also be told "…and short enough to be
// a k8s namespace segment", because the two have different remedies: a bad
// shape is a fragment to be refused, while an over-long name is well-formed
// and merely needs a budget. validateKey wants only the first; Sanitize wants
// both. See validateKey in blocks.go for what the conflation cost.
//
// This function is idempotent, which is what lets a caller use equality with
// its own output as a well-formedness test.

// Sanitize reduces s to a DNS-safe label of bounded length: canonicalLabel
// plus the maxNameLen truncation. This is the form a git FACT takes, and the
// truncation is load-bearing there — see maxNameLen.
//
// Note Sanitize is NOT usable as a well-formedness test, because truncation
// makes it disagree with a perfectly well-formed input purely by length.
// Callers testing shape must use canonicalLabel.
func Sanitize(s string) string {
	s = canonicalLabel(s)
	if len(s) > maxNameLen {
		s = s[:maxNameLen]
		s = leadTrailDA.ReplaceAllString(s, "")
	}
	return s
}

// canonicalLabel is Sanitize's SHAPE half with NO length bound: lowercased,
// [a-z0-9-] only, dash runs collapsed, no leading or trailing dash.
//
// Shape and length are split apart because they are two different rules that
// hold in two different places, and fusing them into one function made every
// caller inherit both whether or not both applied:
//
//   - SHAPE is a hard requirement of every consumer. The value becomes a DNS
//     label, a NATS subject token or a DB name, so "prod_web" or a trailing
//     dash is malformed wherever it lands, at any length.
//   - LENGTH is a budget, and it is a budget on a git FACT being embedded as
//     one segment of a longer composed name. It has no meaning for a
//     port-block key, which is looked up in a map and resolves to an integer.
//
// Truncation is the reason they cannot share a function: it is a legitimate
// repair for a fact (a longer branch name still names the same branch) and an
// unsound one for a key (two different keys can truncate to one, silently
// merging two stacks onto one port block). Only the fact path may truncate.
func canonicalLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonDNS.ReplaceAllString(s, "-")
	s = dashRuns.ReplaceAllString(s, "-")
	return leadTrailDA.ReplaceAllString(s, "")
}

// Worktree returns the LINKED-worktree directory basename for projectDir, or
// "" on the PRIMARY checkout (regardless of branch) and outside any git
// repo. This is the fact a consumer keys on when it wants the primary
// checkout to stay the DEFAULT stack on every branch — the everyday dev
// loop is then byte-identical to today, and only a `git worktree add`'ed
// checkout gets its own stack.
//
// The parallelism unit here is the WORKTREE, never the branch: branches
// change constantly and the primary checkout must always render default.
//
// Detection: a linked worktree's per-worktree git dir
// (`git rev-parse --absolute-git-dir` → …/.git/worktrees/<name>) differs
// from the repo's common dir (`--git-common-dir` → the primary's …/.git).
// The primary checkout has them equal. This is git's own authoritative
// distinction — far more robust than sniffing whether `.git` is a file vs
// a directory (which submodules and some tooling also make a file). The
// returned value is sanitized DNS-safe.
func Worktree(projectDir string) string {
	gitDir := gitOut(projectDir, "rev-parse", "--absolute-git-dir")
	commonDir := gitOut(projectDir, "rev-parse", "--git-common-dir")
	if gitDir == "" || commonDir == "" {
		return "" // not a git checkout
	}
	// Normalize commonDir to absolute so the comparison is exact:
	// --git-common-dir can come back relative (e.g. ".git"), and it is
	// relative to the command's working dir (projectDir), NOT the process
	// cwd — so resolve it against projectDir, not via filepath.Abs.
	if !filepath.IsAbs(commonDir) {
		base := projectDir
		if base == "" {
			if wd, err := os.Getwd(); err == nil {
				base = wd
			}
		}
		commonDir = filepath.Join(base, commonDir)
	}
	if sameDir(gitDir, commonDir) {
		return "" // primary checkout — DEFAULT, regardless of branch
	}
	// Linked worktree. The working-tree basename is the name, EXCEPT when it
	// is not unique among this repo's worktrees — see disambiguate.
	root := gitOut(projectDir, "rev-parse", "--show-toplevel")
	if root == "" {
		return ""
	}
	return disambiguate(projectDir, root, gitDir)
}

// disambiguate returns the worktree's key: normally the working-tree
// basename, but qualified when that basename is shared with another live
// worktree of the same repo.
//
// A bare basename is NOT unique in the nested-repo layout, where every
// worktree is created as <container>/<repo> and the container carries the
// distinguishing name:
//
//	~/worktrees/add-billing/control-plane   -> basename "control-plane"
//	~/worktrees/fix-oauth/control-plane     -> basename "control-plane"
//
// Both resolve to the same key, and the key is the stack's whole identity —
// it selects the port block AND composes the namespace suffix, the DB names
// and the NATS credentials. So two such worktrees do not merely share a port
// block: the second stack renders the first one's namespace and DATABASE
// NAMES, and the two dev stacks quietly become one. That is data loss, not a
// port conflict, and nothing downstream can detect it because by then the two
// stacks are indistinguishable by construction.
//
// The unique-basename case — a plain `git worktree add ../wt-feature`, which
// is what most projects do — is returned UNCHANGED. That matters beyond
// tidiness: the key is memoized in the block registry and multiplied into a
// cluster's pre-mapped host ports and an IdP's baked-in `iss` claim, so
// re-deriving an existing worktree's key to a new string would move a running
// stack's ports. Only a key that is already broken by collision changes.
//
// If the worktree roster cannot be enumerated, the basename is returned as
// before: a failed enumeration is no reason to invent a different key for a
// worktree that may well be fine.
func disambiguate(projectDir, root, gitDir string) string {
	base := Sanitize(filepath.Base(root))
	roots, err := liveWorktreeRoots(projectDir)
	if err != nil {
		return base
	}
	shared := 0
	for _, other := range roots {
		if Sanitize(filepath.Base(other)) == base {
			shared++
		}
	}
	if shared <= 1 {
		return base // unique — today's key, unchanged
	}
	// Qualify with the container directory, which is where the meaningful
	// name lives in this layout ("add-billing", "fix-oauth"). Accept it only
	// if it actually separates the colliding set.
	parent := Sanitize(filepath.Base(filepath.Dir(root)))
	if parent != "" && parent != base {
		unique := true
		for _, other := range roots {
			if other == root || Sanitize(filepath.Base(other)) != base {
				continue
			}
			if Sanitize(filepath.Base(filepath.Dir(other))) == parent {
				unique = false
				break
			}
		}
		if unique {
			return parent
		}
	}
	// Last resort: git's own per-worktree admin directory name, which git
	// guarantees unique (it appends an ordinal on collision). Ugly, but a
	// correct key beats a colliding one.
	if admin := Sanitize(filepath.Base(gitDir)); admin != "" {
		return admin
	}
	return base
}

// liveWorktreeRoots returns the working-tree roots git reports for this repo,
// including the primary checkout. Roots whose directory no longer exists are
// excluded, matching liveWorktreeKeys — a worktree that is gone cannot
// collide with anything.
func liveWorktreeRoots(projectDir string) ([]string, error) {
	out, err := gitWorktreeListPorcelain(projectDir)
	if err != nil {
		return nil, err
	}
	var roots []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		root := strings.TrimPrefix(line, "worktree ")
		if root == "" {
			continue
		}
		if _, statErr := os.Stat(root); statErr != nil {
			continue
		}
		roots = append(roots, root)
	}
	return roots, nil
}

// RepoAnchor returns the directory machine-local, developer-scoped state
// lives under: the PRIMARY checkout's working tree, shared by every linked
// worktree of the same repo. Outside a git repo it returns projectDir
// unchanged, so a non-repo scaffold keeps that state local exactly as before.
//
// Two consumers share this anchor, for the same underlying reason — both
// hold state that is gitignored (so `git worktree add` never materializes
// it) but machine-scoped rather than checkout-scoped:
//
//   - the port-block registry (blocks.go), because a block is a claim
//     against a machine-wide resource; see the argument below.
//   - the dev secret store (internal/secrets), because a fresh worktree
//     would otherwise start with NO values and fail every declared
//     secret_ref, even though the developer set them all in the primary
//     checkout an hour earlier.
//
// WHY THE REGISTRY CANNOT BE PER-WORKTREE. A block is only meaningful as a
// claim against a machine-wide resource: block N means "host ports base+N*100
// on this machine", pre-mapped once in the cluster's port map. A per-directory
// registry cannot express that claim, and it fails in both directions at once:
//
//   - Every fresh worktree starts with an EMPTY registry, so nextFreeBlock
//     hands it block 1 — the block another worktree is already running on.
//     Two stacks then render the identical host ports and the second one
//     silently loses the bind, which is the exact collision allocate_port
//     exists to prevent.
//   - The ceiling counts entries in that empty registry, so it also refuses
//     legitimate NEW blocks while reporting the machine is "full". That is
//     how an 8-stack ceiling refused the FIRST worktree on this machine: the
//     registry it counted had zero entries in it.
//
// Anchoring on the primary checkout makes the registry what the ceiling and
// the pre-map already assume it is — one roster per repo, per machine.
//
// The anchor is git's own `--git-common-dir` (the primary's .git), whose
// PARENT is the primary working tree. That is the same authoritative
// distinction Worktree() uses, so the two can never disagree about which
// checkout is primary.
func RepoAnchor(projectDir string) string {
	commonDir := gitOut(projectDir, "rev-parse", "--git-common-dir")
	if commonDir == "" {
		return projectDir // not a git checkout — keep the registry local
	}
	if !filepath.IsAbs(commonDir) {
		base := projectDir
		if base == "" {
			if wd, err := os.Getwd(); err == nil {
				base = wd
			}
		}
		commonDir = filepath.Join(base, commonDir)
	}
	// A bare repo has no working tree to anchor to; fall back to projectDir
	// rather than writing beside the bare .git.
	if filepath.Base(commonDir) != ".git" {
		return projectDir
	}
	return filepath.Dir(commonDir)
}

// SharedProjectDir returns projectDir's counterpart in the repo's PRIMARY
// checkout, as an absolute path: the directory a machine-shared resource is
// driven from, whichever worktree forge runs in.
//
// It is RepoAnchor plus projectDir's offset within its own working tree, so a
// forge project nested in a monorepo (`<repo>/services/api`) maps to
// `<primary>/services/api`, not to the primary's repo root. Outside a repo,
// or where the anchor falls back to projectDir (a bare repo), projectDir is
// its own shared directory.
//
// Shared compose infrastructure (OnCompose.shared) and the files it mounts
// (fp.write_file(..., shared=True)) resolve here. Driving one compose project
// from N directories that share a basename is what made a linked worktree
// recreate every shared container: compose resolves relative bind mounts
// against the directory it runs from, so each checkout produced a different
// config for the SAME project.
func SharedProjectDir(projectDir string) string {
	anchor := RepoAnchor(projectDir)
	if !sameDir(anchor, projectDir) {
		if prefix := gitOut(projectDir, "rev-parse", "--show-prefix"); prefix != "" {
			anchor = filepath.Join(anchor, filepath.FromSlash(prefix))
		}
	}
	if abs, err := filepath.Abs(anchor); err == nil {
		return abs
	}
	return anchor
}

// Branch returns the current git branch for projectDir, sanitized DNS-safe,
// or "" outside a repo or on a detached HEAD. Unlike Worktree, Branch is
// reported for the primary checkout too — a consumer that WANTS to key on
// branch (e.g. a stack-per-branch workflow) can; one that wants the primary
// checkout to stay default keys on Worktree instead. The author chooses.
func Branch(projectDir string) string {
	b := gitOut(projectDir, "rev-parse", "--abbrev-ref", "HEAD")
	if b == "" || b == "HEAD" { // "HEAD" == detached
		return ""
	}
	return Sanitize(b)
}

// sameDir reports whether two paths point at the same directory, tolerant
// of symlinks (macOS /var vs /private/var) and trailing-slash differences.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	if ea == nil && eb == nil {
		return filepath.Clean(ra) == filepath.Clean(rb)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func gitOut(dir string, args ...string) string {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
