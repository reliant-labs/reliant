package cli

// The pre-build refusal of a go.work-bridged forge the image cannot compile
// against (forge report affcefc1).
//
// WHY. A project made, or regenerated, by a forge built from a local checkout
// compiles on the host against that checkout: `forge project new` (and a
// developer's own go.work) bridges github.com/reliant-labs/forge to it with a
// `use`, and go.mod requires the newest RELEASE the checkout descends from
// (generator.resolveForgeVersion). The project image never sees the bridge.
// Its Dockerfile builds with GOWORK=off from the committed module graph, so it
// compiles against the go.mod pin — which lacks whatever the checkout added
// since that release. The generated code calls it, and the docker build fails
// minutes in with `undefined: devidp.HandoffStartPath`, naming nothing about
// why. `env build` and `env deploy` stop before any build instead, naming the
// go.mod line, the bridge, and the `go get` that makes the pin the checkout.
//
// WHAT IT DECIDES, offline: from go.mod, go.work (or $GOWORK) and the bridged
// checkout's git refs. Never the module proxy.
//
//   - no go.work, GOWORK=off, no forge `use`/`replace` in it, or the bridged
//     checkout inside the build context: nothing to refuse (the project's
//     behaviour before this check);
//   - go.mod replaces forge with a local path outside the build context: the
//     image cannot see that path at all — refused;
//   - otherwise go.mod's pin is resolved in the checkout (a tag, or a
//     pseudo-version's commit). The pin compiles what the host compiled when
//     the checkout's HEAD is the pin or an ancestor of it; refused when HEAD
//     has commits the pin lacks, or when the pin is HEAD's pseudo-version and
//     no remote branch carries that commit (the proxy can never serve it). A
//     pin the checkout cannot resolve is not judged: that needs the network.
//
// A project image whose Dockerfile compiles nothing (it COPYs host-built
// binaries) is not judged either: the host build saw the bridge.
//
// The hatch, for a build context that does provide the module (a Dockerfile
// that copies a named build context into place): FORGE_ALLOW_LOCAL_FORGE_BRIDGE=1.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/reliant-labs/forge/internal/forgecompat"
)

// allowLocalForgeBridgeEnv skips the check (any non-empty value).
const allowLocalForgeBridgeEnv = "FORGE_ALLOW_LOCAL_FORGE_BRIDGE"

// dockerfileCompilesGo matches a Dockerfile that builds Go in the image —
// the only kind the go.mod pin decides.
var dockerfileCompilesGo = regexp.MustCompile(`(?m)\bgo\s+(build|install|mod\s+download)\b`)

// forgeBridge is a go.work route from forge's module to a local checkout.
type forgeBridge struct {
	dir   string // the checkout, absolute
	where string // "go.work:7", for the message
}

// forgeBridgeGit is the git the check reads in the bridged checkout. A seam so
// the decision is tested without repositories; realForgeBridgeGit is git.
type forgeBridgeGit interface {
	// head is the checkout's HEAD commit, "" when it is not a repository.
	head(dir string) string
	// resolve is the commit rev names in the checkout, "" when unknown.
	resolve(dir, rev string) string
	// isAncestor reports whether a is b or an ancestor of b.
	isAncestor(dir, a, b string) bool
	// published reports whether some remote-tracking branch contains sha;
	// known is false when the checkout has no remote to tell by.
	published(dir, sha string) (ok, known bool)
}

var bridgeGit forgeBridgeGit = realForgeBridgeGit{}

// preflightForgeBridge is runBuild's call: only a build that opted in
// (checkForgeBridge) and builds the project image is judged.
func preflightForgeBridge(opts buildOptions, targets buildTargetSet) error {
	if !opts.checkForgeBridge || !opts.buildDocker || len(targets.goTargets) == 0 || targets.skipProjectDocker {
		return nil
	}
	dir := projectDirForKCL()
	return refuseUnbuildableForgeBridge(dir, projectImageCompilesGo(filepath.Join(dir, "Dockerfile")))
}

// refuseUnbuildableForgeBridge returns the refusal for projectDir, or nil.
// compilesInImage is whether the project image compiles Go itself.
func refuseUnbuildableForgeBridge(projectDir string, compilesInImage bool) error {
	if !compilesInImage || os.Getenv(allowLocalForgeBridgeEnv) != "" {
		return nil
	}
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return nil
	}
	bridge, ok := findForgeBridge(root)
	if !ok || within(root, bridge.dir) {
		return nil
	}
	gomod := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		return nil
	}
	mf, err := modfile.Parse(gomod, data, nil)
	if err != nil {
		return nil
	}
	pin, line := "", 0
	for _, r := range mf.Require {
		if r.Mod.Path == forgecompat.ModulePath {
			pin, line = r.Mod.Version, modLine(r.Syntax)
		}
	}
	for _, rp := range mf.Replace {
		if rp.Old.Path != forgecompat.ModulePath || (rp.Old.Version != "" && rp.Old.Version != pin) {
			continue
		}
		if rp.New.Version == "" { // a local path
			target := rp.New.Path
			if !filepath.IsAbs(target) {
				target = filepath.Join(root, target)
			}
			if within(root, target) {
				return nil
			}
			return bridgeRefusal(root, bridge, fmt.Sprintf("go.mod:%d replaces %s with %s, a directory outside the build context (%s), which the image cannot see",
				modLine(rp.Syntax), forgecompat.ModulePath, rp.New.Path, root), bridgeGit.head(bridge.dir), bridge.dir)
		}
		pin, line = rp.New.Version, modLine(rp.Syntax)
	}
	if pin == "" {
		return nil
	}
	head := bridgeGit.head(bridge.dir)
	if head == "" {
		return nil
	}
	rev := pin
	if module.IsPseudoVersion(pin) {
		if r, err := module.PseudoVersionRev(pin); err == nil {
			rev = r
		}
	} else {
		rev = "refs/tags/" + pin
	}
	pinCommit := bridgeGit.resolve(bridge.dir, rev)
	if pinCommit == "" {
		return nil // undecidable offline
	}
	pinLine := fmt.Sprintf("go.mod:%d requires %s %s", line, forgecompat.ModulePath, pin)
	if pinCommit == head {
		if ok, known := bridgeGit.published(bridge.dir, head); module.IsPseudoVersion(pin) && known && !ok {
			return bridgeRefusal(root, bridge, pinLine+", a pseudo-version of a commit no remote branch carries, so the image's module download cannot fetch it", head, bridge.dir)
		}
		return nil
	}
	if bridgeGit.isAncestor(bridge.dir, head, pinCommit) {
		return nil // the pin is the checkout or newer
	}
	return bridgeRefusal(root, bridge, pinLine+fmt.Sprintf(", but the checkout the host compiles against is at %s, with commits that pin lacks; the image builds with GOWORK=off from go.mod, so it compiles the pin, not the checkout", short(head)), head, bridge.dir)
}

// bridgeRefusal is the error: what was found, and the exact fixes.
func bridgeRefusal(root string, b forgeBridge, problem, head, checkout string) error {
	var fix strings.Builder
	if head != "" {
		fmt.Fprintf(&fix, "  pin the checkout's commit (it must be pushed for the image's download to fetch it):\n      go get %s@%s && go mod tidy\n", forgecompat.ModulePath, head)
		if strings.Contains(string(bridgeReadFile(filepath.Join(root, "gen", "go.mod"))), forgecompat.ModulePath) {
			fmt.Fprintf(&fix, "    and the same in gen/: (cd gen && go get %s@%s && go mod tidy)\n", forgecompat.ModulePath, head)
		}
		if ok, known := bridgeGit.published(checkout, head); known && !ok {
			fmt.Fprintf(&fix, "    %s is on no remote branch yet: push it first (git -C %s push).\n", short(head), checkout)
		}
	}
	fmt.Fprintf(&fix, "  or copy the checkout inside the build context and point go.mod at it:\n      replace %s => ./<dir inside the project>\n", forgecompat.ModulePath)
	fmt.Fprintf(&fix, "  or, when the build context does provide the module (a Dockerfile that copies a named build context into place), run with %s=1", allowLocalForgeBridgeEnv)
	return fmt.Errorf("the project image cannot compile against the forge this project is built with: %s bridges %s to %s, outside the build context.\n%s.\nFix, before any build:\n%s",
		b.where, forgecompat.ModulePath, b.dir, problem, fix.String())
}

// findForgeBridge reads the go.work the go command would use from root ($GOWORK,
// else the nearest go.work at or above root) for a `use` or `replace` routing
// forge's module to a local directory.
func findForgeBridge(root string) (forgeBridge, bool) {
	path := strings.TrimSpace(os.Getenv("GOWORK"))
	switch path {
	case "off":
		return forgeBridge{}, false
	case "":
		for dir := root; ; dir = filepath.Dir(dir) {
			if st, err := os.Stat(filepath.Join(dir, "go.work")); err == nil && !st.IsDir() {
				path = filepath.Join(dir, "go.work")
				break
			}
			if filepath.Dir(dir) == dir {
				return forgeBridge{}, false
			}
		}
	}
	// The go command takes $GOWORK only as an absolute path: a relative one
	// fails every go invocation ("go: invalid GOWORK: not an absolute
	// path"), so no build — host or image — compiles through it. Resolving it
	// against this process's working directory would inspect a workspace the
	// go command never selects.
	if !filepath.IsAbs(path) {
		return forgeBridge{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return forgeBridge{}, false
	}
	wf, err := modfile.ParseWork(path, data, nil)
	if err != nil {
		return forgeBridge{}, false
	}
	base := filepath.Dir(path)
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(base, p)
	}
	where := func(l int) string { return fmt.Sprintf("%s:%d", path, l) }
	for _, rp := range wf.Replace {
		if rp.Old.Path == forgecompat.ModulePath && rp.New.Version == "" {
			return forgeBridge{dir: abs(rp.New.Path), where: where(modLine(rp.Syntax))}, true
		}
	}
	for _, u := range wf.Use {
		dir := abs(u.Path)
		if modulePathOf(filepath.Join(dir, "go.mod")) == forgecompat.ModulePath {
			return forgeBridge{dir: dir, where: where(modLine(u.Syntax))}, true
		}
	}
	return forgeBridge{}, false
}

func bridgeReadFile(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

func modulePathOf(gomod string) string {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	return modfile.ModulePath(data)
}

func modLine(l *modfile.Line) int {
	if l == nil {
		return 0
	}
	return l.Start.Line
}

// projectImageCompilesGo reports whether the project Dockerfile at path builds
// Go inside the image. An unreadable one is judged as the scaffold, which does.
func projectImageCompilesGo(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	return dockerfileCompilesGo.Match(data)
}

type realForgeBridgeGit struct{}

func (realForgeBridgeGit) out(dir string, args ...string) (string, bool) {
	b, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(b)), err == nil
}

func (g realForgeBridgeGit) head(dir string) string {
	s, _ := g.out(dir, "rev-parse", "--verify", "--quiet", "HEAD")
	return s
}

func (g realForgeBridgeGit) resolve(dir, rev string) string {
	s, _ := g.out(dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return s
}

func (g realForgeBridgeGit) isAncestor(dir, a, b string) bool {
	_, ok := g.out(dir, "merge-base", "--is-ancestor", a, b)
	return ok
}

func (g realForgeBridgeGit) published(dir, sha string) (bool, bool) {
	if remotes, _ := g.out(dir, "remote"); remotes == "" {
		return false, false
	}
	s, ok := g.out(dir, "branch", "-r", "--contains", sha)
	return ok && s != "", ok
}
