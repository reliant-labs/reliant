package forgecompat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/goexec"
)

// "Is this the same forge?" is a question about SOURCE, and a version string
// is only a name for it. One commit has several names:
//
//   - the release tag on it (v1.0.9) and the pseudo-version Go synthesises for
//     it from an older base (v1.0.9-0.20261030224330-36d0220bfb60);
//   - two pseudo-versions with different bases, because the base is the
//     nearest tag the deriving side could see: a clone that has fetched
//     v1.0.14 derives v1.0.15-0.<ts>-<sha> for the commit a go.mod pinned,
//     before that tag existed, as v1.0.14-0.<ts>-<sha>.
//
// Comparing the strings refused every one of those pairs: a deploy cut a
// duplicate, byte-different release because the forge that built the first
// one spelled its version differently (forge reports fca68889, a68a8644), and
// a KCL import of a sibling project failed with "version skew" between a tag
// and a pseudo-version of one commit (304e886b). SameForge compares what the
// names point at.

// SameForge reports whether versions a and b name the same forge source: the
// same string, or two names of one commit. A pseudo-version names its commit
// itself; a release tag is resolved through the Go module cache, where the go
// command records the commit of every version it fetched. A version carrying
// +dirty names a working tree, not a commit, so it matches nothing, not even
// itself (two dirty trees of one commit share the name); and a
// version that resolves to no commit (an unfetched tag, "(devel)") matches
// only itself too — an unproven identity is a different forge.
func SameForge(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	// Dirty first: two forges built from dirty trees of one commit report
	// the same string, yet their working trees (and so their output) may
	// differ, so not even an identical dirty version proves one source.
	if isDirty(a) || isDirty(b) {
		return false
	}
	if a == b {
		return true
	}
	ca, cb := VersionCommit(a), VersionCommit(b)
	return sameRevision(ca, cb)
}

// VersionCommit is the commit a forge version names: a pseudo-version's
// revision (12 hex), or for a release tag the full hash the module cache
// recorded when the go command fetched it. "" when v names no commit this
// machine can prove: "(devel)", a +dirty tree, or a tag never fetched here.
func VersionCommit(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || isDirty(v) {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	if module.IsPseudoVersion(v) {
		rev, err := module.PseudoVersionRev(v)
		if err != nil {
			return ""
		}
		return strings.ToLower(rev)
	}
	if semver.Build(v) != "" {
		return "" // `+dev` on a non-pseudo version describes a working tree
	}
	return tagCommit(v)
}

// DirtyVersion reports whether forge version v carries +dirty build metadata:
// it names a build from a modified working tree, not a commit. Nothing can
// fetch or rebuild such a forge — `go install …@v` drops the build metadata
// and builds the clean commit, without the edits — and two binaries that
// report the same dirty version need not hold the same code.
func DirtyVersion(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && isDirty(v)
}

func isDirty(v string) bool {
	return strings.Contains(semver.Build(ensureV(v)), "dirty")
}

func ensureV(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// tagCommit reads the commit the go command recorded for forge@tag in the
// module cache's download metadata ($GOMODCACHE/cache/download/<module>/@v/
// <tag>.info, field Origin.Hash). Nothing is fetched: a check that decides
// whether two pins agree must not need the network, and a pin this machine
// never resolved cannot be the one it is running.
func tagCommit(tag string) string {
	dir := forgeModuleCacheDir()
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, tag+".info"))
	if err != nil {
		return ""
	}
	var info struct {
		Origin *struct {
			Hash string `json:"Hash"`
		} `json:"Origin"`
	}
	if json.Unmarshal(data, &info) != nil || info.Origin == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(info.Origin.Hash))
}

// forgeModuleCacheDir is the module cache's download directory for forge's
// module. A package variable so tests can point it at a fixture.
var forgeModuleCacheDir = func() string {
	root := goModCache()
	if root == "" {
		return ""
	}
	escaped, err := module.EscapePath(ModulePath)
	if err != nil {
		return ""
	}
	return filepath.Join(root, "cache", "download", filepath.FromSlash(escaped), "@v")
}

var goModCache = sync.OnceValue(func() string {
	// `go env` honours `go env -w`, which the variables below do not. From a
	// neutral directory: inside a module that names a newer toolchain, even
	// `go env` would switch (and maybe download) toolchains.
	cmd := exec.Command("go", "env", "GOMODCACHE")
	cmd.Dir = os.TempDir()
	cmd.Env = goexec.Env()
	if out, err := cmd.Output(); err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" {
			return dir
		}
	}
	if dir := strings.TrimSpace(os.Getenv("GOMODCACHE")); dir != "" {
		return dir
	}
	gopath := strings.TrimSpace(os.Getenv("GOPATH"))
	if gopath != "" {
		gopath = filepath.SplitList(gopath)[0]
	} else if home, err := os.UserHomeDir(); err == nil {
		gopath = filepath.Join(home, "go")
	} else {
		return ""
	}
	return filepath.Join(gopath, "pkg", "mod")
})
