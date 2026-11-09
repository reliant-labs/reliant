package ledgerfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// repoHashLen is how much of the remote's hash goes in the project id. 12 hex
// is 48 bits: ample against accidental collision among the handful of repos
// one machine holds, and short enough that the directory name stays readable
// (control-plane-9f2a1c4b8e07).
const repoHashLen = 12

// ProjectID is the ledger directory name for one project: its forge.yaml
// name, plus a hash of its canonical remote URL when it has one.
//
// WHY THE NAME ALONE IS NOT ENOUGH. Two unrelated checkouts can both be
// called "api" — a fork, a vendored copy, two customers' projects on one
// consultant's laptop — and merging their ledgers would have one project's
// promotions answer the other's "what does prod run". So the remote, which
// is the only durable identity a project has, is folded in.
//
// WHY THE PATH IS NOT IN IT, WHICH IS THE WHOLE POINT. Every worktree of a
// project shares one id and therefore one ledger, so the answer to "what
// does prod run" does not depend on which directory or branch you happen to
// be standing in. release.CanonicalRepo strips scheme, credentials, port
// and the .git suffix, so git@github.com:o/r.git and https://github.com/o/r
// hash the same — the same repo reached two ways is one project.
//
// A project with NO remote gets the bare name. That is correct rather than a
// fallback: a never-pushed project has no identity beyond its name on this
// machine, and inventing one from its path would re-introduce the
// per-directory split this design exists to remove.
func ProjectID(name, remoteURL string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("ledger project id: forge.yaml has no name, so the ledger has nothing to be keyed by")
	}
	stem := safeSegment(name)
	repo := release.CanonicalRepo(remoteURL)
	if repo == "" {
		return stem, nil
	}
	sum := sha256.Sum256([]byte(repo))
	return stem + "-" + hex.EncodeToString(sum[:])[:repoHashLen], nil
}

// safeSegment flattens anything that could escape the ledger home or confuse
// a shell into '_'. A project id becomes a directory name, so this is a
// containment guard, not cosmetics: a name of "../../etc" must not resolve
// outside $FORGE_LEDGER_HOME. A name that flattens to nothing, or to dots
// alone (a path reference rather than a name), becomes "_".
func safeSegment(s string) string {
	out := make([]byte, 0, len(s))
	allDots := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
			allDots = false
		case c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
			allDots = false
		}
	}
	if len(out) == 0 || allDots {
		return "_"
	}
	return string(out)
}
