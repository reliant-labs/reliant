package release

import (
	"fmt"
	"regexp"
	"time"
)

// Provenance is WHERE a release or a bundle came from: the source tree, the
// checkout it was built in, and the forge that rendered it. It is shared by
// releases (the images) and bundles (the config), so "images from main@abc,
// config from feat-x@def, dirty" is two values of one type.
//
// # Claims and checkable facts
//
// What a CLIENT may claim and what a SERVER can establish are kept apart, and
// every reader must keep them apart too:
//
//   - Commit is checkable against the repository.
//   - Tree is checkable when Dirty is false: a clean tree's hash IS the
//     commit's tree, so a clean claim can be verified without trusting the
//     client.
//   - Branch, Worktree, Dirty and CommitTime are claims. They are evidence and are
//     rendered as such ("reported by forge"), never used as authority.
//   - Ancestry ("is this commit on main") is never a field here. It changes
//     over time — a branch is merged, a force-push orphans a commit — so it
//     cannot sit on an immutable record. A backend derives it.
//
// Release.Git predates this type and is kept, filled from it (see
// [Release.SetProvenance]), so ledgers written before Provenance existed
// still decode and readers that only know Git keep working.
type Provenance struct {
	// Repo is the canonical remote, e.g. "github.com/reliant-labs/forge".
	// Empty when the checkout has no remote.
	Repo string `json:"repo"`
	// Commit is HEAD, full hex. Empty when the source is not a git tree.
	Commit string `json:"commit"`
	// CommitTime is when Commit was made: the committer date, UTC. It is the
	// start of a lead-time measurement (commit to production), so it is only
	// ever the time of the commit that was BUILT: nil when the checkout was
	// dirty (the bytes are no commit's) or the commit is unknown. It is a
	// claim like Branch, not authority.
	CommitTime *time.Time `json:"commit_time,omitempty"`
	// CommitSubject is Commit's subject line (the first line of its message)
	// and CommitAuthor its author's name, as git reported them, so a release
	// can be read as "abc1234 fix(gateway): …, by alice" without a repository
	// at hand. Set whenever Commit is, dirty or not: a dirty build's Commit is
	// the base it was built on, and Dirty says the bytes are more than that.
	// Claims like Branch. The subject is bounded at [MaxCommitSubjectBytes].
	CommitSubject string `json:"commit_subject,omitempty"`
	CommitAuthor  string `json:"commit_author,omitempty"`
	// Branch is the symbolic ref at build time; empty on a detached HEAD.
	Branch string `json:"branch,omitempty"`
	// Tag is an exact tag at HEAD, if any.
	Tag string `json:"tag,omitempty"`
	// Dirty: tracked OR untracked-unignored changes existed at build time.
	Dirty bool `json:"dirty"`
	// Tree is the git tree hash of the BUILT content, dirty changes
	// included, computed through a temporary index (the user's index is
	// never touched). Empty means it was not hashed — no git, or the
	// working tree was too large to hash within the capture budget. A
	// clean checkout's Tree equals HEAD^{tree}.
	Tree string `json:"tree"`
	// Worktree identifies the checkout on its machine.
	Worktree Worktree `json:"worktree"`
	// ForgeVersion is the binary that rendered. A render is a function of
	// (forge, KCL, config), so the same commit rendered by two forges is
	// two different artifacts.
	ForgeVersion string `json:"forge_version"`
	// Attestation is the CI identity that built it, or nil from a laptop.
	Attestation *Attest `json:"attestation,omitempty"`
}

// Worktree identifies one checkout. None of it is authority.
type Worktree struct {
	// Key is forge's devstack key: "" for the primary checkout, the
	// sanitized linked-worktree name otherwise. It is the same key the
	// checkout's dev-stack port block is held under.
	Key string `json:"key"`
	// Label is what a human calls the checkout: the branch, else the
	// directory's base name. Never a path.
	Label string `json:"label,omitempty"`
	// Host names the machine: a daemon id, or a hash of the hostname.
	// Never a raw hostname or path.
	Host string `json:"host,omitempty"`
	// Path is the checkout's absolute path, for DISPLAY on the machine that
	// built it. It is stripped from anything sent to a hosted ledger
	// ([Provenance.ForHosted]): a path names a user's home directory.
	Path string `json:"path,omitempty"`
}

// Attest is a CI identity claim (an OIDC subject). A hosted backend verifies
// it before it means anything; a file ledger records it as stated.
type Attest struct {
	Provider string `json:"provider"`
	Subject  string `json:"subject"`
	RunID    string `json:"run_id,omitempty"`
}

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	treePattern   = commitPattern
)

// ValidObjectID reports whether s is a full git object id (SHA-1 or SHA-256).
func ValidObjectID(s string) bool { return commitPattern.MatchString(s) }

// Validate checks the shape of the claims. It cannot check their truth.
func (p Provenance) Validate() error {
	if p.Commit != "" && !commitPattern.MatchString(p.Commit) {
		return fmt.Errorf("%w: provenance commit %q is not a full git object id", ErrInvalid, p.Commit)
	}
	if p.Tree != "" && !treePattern.MatchString(p.Tree) {
		return fmt.Errorf("%w: provenance tree %q is not a full git object id", ErrInvalid, p.Tree)
	}
	if p.Tree != "" && p.Commit == "" {
		return fmt.Errorf("%w: provenance names a tree but no commit", ErrInvalid)
	}
	if p.CommitTime != nil && p.Commit == "" {
		return fmt.Errorf("%w: provenance names a commit time but no commit", ErrInvalid)
	}
	if (p.CommitSubject != "" || p.CommitAuthor != "") && p.Commit == "" {
		return fmt.Errorf("%w: provenance names a commit subject or author but no commit", ErrInvalid)
	}
	if err := validateCommitText("provenance commit subject", p.CommitSubject, MaxCommitSubjectBytes); err != nil {
		return err
	}
	if err := validateCommitText("provenance commit author", p.CommitAuthor, MaxCommitAuthorBytes); err != nil {
		return err
	}
	if p.Attestation != nil && (p.Attestation.Provider == "" || p.Attestation.Subject == "") {
		return fmt.Errorf("%w: provenance attestation needs a provider and a subject", ErrInvalid)
	}
	return nil
}

// Git is the legacy three-field view of this provenance.
func (p Provenance) Git() Git {
	return Git{Commit: p.Commit, Tag: p.Tag, Dirty: p.Dirty}
}

// ForHosted is the copy that may leave this machine: the worktree path is
// dropped. Everything else is already path-free by construction.
func (p Provenance) ForHosted() Provenance {
	p.Worktree.Path = ""
	return p
}

// Unhashed reports whether this provenance names a commit but could not hash
// what was built (F-16). Such a record is honest about the gap rather than
// pretending the tree is HEAD's.
func (p Provenance) Unhashed() bool { return p.Commit != "" && p.Tree == "" }
