// Package forgecompat decides whether the running forge binary may generate
// into a project, from the forge version that project ACTUALLY resolves.
//
// The generator emits code that calls into forge/pkg/* — orm, crud, testkit,
// serverkit. Those are packages in the SINGLE forge module, so the library
// that code compiles against is whatever version the project requires, and
// the symbols it may call are the ones that existed as of THIS binary's
// version. A project pinning a forge OLDER than this binary can therefore
// lack symbols the generated code names.
//
// WHY THIS IS A VERSION COMPARISON AND NOT A SYMBOL PROBE. It used to compile
// a throwaway program against the project's forge/pkg, exercising a
// hand-maintained list of symbols that whoever taught an emitter to call
// something new was expected to extend. That list rotted: forge grew
// `testkit.StubNotConfigured`, nobody added the entry, and a control-plane
// `forge generate` failed in validate with the tree already rewritten.
//
// Generator and runtime ship as one module, so their versions are one number
// and the honest check is an inequality over it: the project's forge must be
// >= the forge doing the generating. That covers every symbol ever added,
// with no registry to forget, and costs one `go list`.
//
// A project resolving forge NEWER than this binary is allowed through: older
// templates calling into a newer library is the ordinary upgrade order.
//
// It lives in its own package, rather than inside the generate command,
// because more than generate needs the answer: `forge env render` advises
// running `forge generate` to refresh a stale .forge-kcl/, and that advice is
// wrong whenever generate would refuse.
package forgecompat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/goexec"
)

// ModulePath is the module a project requires to get forge's runtime
// libraries. `forge/pkg` is a package prefix inside it, not a module.
const ModulePath = "github.com/reliant-labs/forge"

// Verdict is the outcome of the version comparison, split from both the
// toolchain query and any error prose so the decision table can be tested
// without a module graph.
type Verdict int

const (
	// OK means generating is safe (or the question could not be answered,
	// and guessing is worse than letting generate's validate step speak).
	OK Verdict = iota
	// UnreleasableNoBridge means this binary exists on no module proxy and
	// the project resolves forge to a published version — the pairing that
	// produced `undefined: testkit.StubNotConfigured`.
	UnreleasableNoBridge
	// StalePin means the project's forge is older than the binary
	// generating into it.
	StalePin
	// BehindPin means the running binary is OLDER than the project's pin.
	// Unlike StalePin this is not about the Go library half — forge's KCL
	// schema module is embedded in the binary, so an older binary renders
	// the project's KCL against an older schema and reports the missing
	// field against the PROJECT's line number. See skew.go.
	BehindPin
)

// Assessment is the verdict plus the two versions it was computed from, so
// a caller can name them in its own message.
type Assessment struct {
	Verdict Verdict
	// ProjectVersion is the forge version the project resolves; "" when it
	// resolves to a local directory (a go.work `use` or directory replace).
	ProjectVersion string
	// BinaryVersion is buildinfo.InstallableVersion(): "" for a build no
	// module proxy can serve.
	BinaryVersion string
}

// Assess answers the question for projectDir. ok is false whenever it does
// not apply or cannot be answered — no go.mod, no forge requirement, or a
// toolchain that will not say how forge resolves.
func Assess(projectDir string) (a Assessment, ok bool) {
	data, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil || !strings.Contains(string(data), ModulePath) {
		return Assessment{}, false
	}
	projectVersion, local, resolved := ResolveProjectForge(projectDir)
	if !resolved {
		return Assessment{}, false
	}
	binaryVersion := buildinfo.InstallableVersion()
	return Assessment{
		Verdict:        Decide(binaryVersion, buildinfo.Version(), projectVersion, local),
		ProjectVersion: projectVersion,
		BinaryVersion:  binaryVersion,
	}, true
}

// Decide is the pure decision.
//
// binaryVersion is buildinfo.InstallableVersion() — "" for a build no proxy
// can serve. rawBuildVersion is buildinfo.Version(). projectVersion/local
// describe how forge resolves IN the project.
//
// A local resolution always passes HERE: the project compiles against source,
// so there is no version to be behind. Whether this binary was built from that
// source is a different question, answered by InspectBridge (bridge.go) and
// reported by generate, lint and doctor. An unknown projectVersion also passes
// — guessing is worse than letting validate speak.
func Decide(binaryVersion, rawBuildVersion, projectVersion string, local bool) Verdict {
	if local {
		return OK
	}
	if binaryVersion == "" {
		// THE PROJECT'S OWN RESOLUTION IS PROOF OF AVAILABILITY, and it beats
		// this binary's inability to vouch for itself.
		//
		// InstallableVersion() returns "" for anything built from a working
		// tree, because a local checkout cannot prove its commit was pushed.
		// That is the right default and it stays. But when the project
		// ALREADY resolves forge to the very version this binary reports,
		// the proof exists: `go list -m` answered from the module graph, so
		// the proxy served it. Refusing there is a false positive with a
		// self-contradicting message — it names one version as both "what
		// this forge is" and "the published version the project resolves to"
		// and then calls them incompatible.
		//
		// Hit in practice pinning control-plane to a pushed forge BRANCH
		// commit: `task pin:forge` resolved the pseudo-version, go.mod,
		// forge.yaml and .forge-kcl all agreed, and generate still refused —
		// telling the user to bridge with go.work when nothing needed
		// bridging.
		if projectVersion != "" && projectVersion == rawBuildVersion {
			return OK
		}
		return UnreleasableNoBridge
	}
	if projectVersion == "" {
		return OK
	}
	if semver.Compare(projectVersion, binaryVersion) < 0 {
		return StalePin
	}
	// The project is NEWER than the binary. For the Go half that is the
	// ordinary upgrade order (see the package header), but forge's KCL
	// schema module is embedded in THIS binary, so the render would
	// evaluate the project's KCL against an older schema and blame the
	// project's own line number for a field it is entitled to use. That is
	// the skew this verdict exists to name — see skew.go.
	if BinaryBehindPin(projectVersion, binaryVersion) && !SkewOverridden() {
		return BehindPin
	}
	return OK
}

// ResolveProjectForge asks the go toolchain how forge ACTUALLY resolves for
// this project — which is not necessarily what go.mod says, because a go.work
// or a replace can override it.
//
// local is true when forge resolves to a directory on disk (a workspace `use`
// or a directory `replace`) rather than a published version: a module in a
// workspace has no version, which is the signal. ok is false when the
// toolchain could not answer at all.
func ResolveProjectForge(projectDir string) (version string, local, ok bool) {
	cmd := exec.Command("go", "list", "-m",
		"-f", "{{.Version}}|{{with .Replace}}{{.Version}}|{{.Path}}{{end}}",
		ModulePath)
	cmd.Dir = projectDir
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return "", false, false
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "|")
	version = strings.TrimSpace(fields[0])
	// A replace wins: its version (empty for a directory target) is what the
	// build actually compiles.
	if len(fields) >= 3 {
		replaceVersion, replacePath := strings.TrimSpace(fields[1]), strings.TrimSpace(fields[2])
		if replaceVersion == "" && replacePath != "" {
			return "", true, true // replaced by a directory
		}
		if replaceVersion != "" {
			version = replaceVersion
		}
	}
	if version == "" {
		return "", true, true // in the workspace: no version at all
	}
	if !semver.IsValid(version) {
		return "", false, false
	}
	return version, false, true
}
