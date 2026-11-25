package forgecompat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

// This file owns the OTHER direction of version skew from forgecompat.go's:
// a binary OLDER than the project's pin.
//
// forgecompat.go's StalePin reasons about the Go half — generated code
// calling into forge/pkg/*, which resolves through the project's module
// graph. There, a project newer than the binary is the ordinary upgrade
// order and fine.
//
// The KCL half does not work that way. forge's KCL schema module is
// EMBEDDED IN THE BINARY (internal/kclvendor materializes it from
// //go:embed), so the schema a render evaluates against is the binary's, not
// the project's. A project that declares forge v0.1.43 is entitled to every
// field v0.1.43's schemas have; rendering it with a v0.1.42 binary evaluates
// its KCL against v0.1.42's schemas, and KCL reports the missing field as an
// error ON THE PROJECT'S LINE:
//
//	deploy/kcl/dev/main.k:2933: Cannot add member 'runtime_type' to schema 'Workload'
//
// That message is true about what KCL saw and wrong about whose mistake it
// is, and it names neither version. It cost an agent real time reading line
// 2933 as the fault. Naming the skew is the whole fix.

// SkewOverrideEnv lets a user proceed past the refusal. Pre-1.0 forge owes
// no compatibility, but a wall with no door is its own defect — someone
// debugging forge itself, or rendering a project whose pin is wrong, needs
// to get through. Set to any non-empty value.
const SkewOverrideEnv = "FORGE_ALLOW_VERSION_SKEW"

// SkewOverridden reports whether the user has asked to proceed despite a
// version skew.
func SkewOverridden() bool {
	return strings.TrimSpace(os.Getenv(SkewOverrideEnv)) != ""
}

// firstUserSentinel is the deliberate forge.yaml pin for forge-as-its-own-
// first-user: the repo builds itself and pins 0.0.0 so `forge project
// upgrade` always surfaces migrations. It is never a real pin and must never
// be compared as one — see auditVersion, which makes the same exception.
const firstUserSentinel = "0.0.0"

// comparablePins reports whether a pinned/binary pair can be ordered at all,
// returning them semver-normalized.
//
// Both halves have to be real releases. A "(devel)" / `+dirty` /
// pseudo-version binary names no release and is the COMMON local case —
// forge builds itself from a working tree — so comparing it would fire this
// diagnostic on every dogfood render. A diagnostic that cries wolf is why
// nobody reads the real one.
func comparablePins(pinned, binary string) (p, b string, ok bool) {
	pinned, binary = strings.TrimSpace(pinned), strings.TrimSpace(binary)
	if pinned == "" || pinned == firstUserSentinel || binary == "" {
		return "", "", false
	}
	if buildinfo.IsDevVersion(binary) || isPseudoVersion(binary) {
		return "", "", false
	}
	if !strings.HasPrefix(pinned, "v") {
		pinned = "v" + pinned
	}
	if !strings.HasPrefix(binary, "v") {
		binary = "v" + binary
	}
	if !semver.IsValid(pinned) || !semver.IsValid(binary) {
		return "", "", false
	}
	return pinned, binary, true
}

// isPseudoVersion reports whether v is a Go pseudo-version — a version
// synthesised for a commit, which names no release to compare a pin against.
// Matched on the SUFFIX (14-digit timestamp + 12-hex commit) so both forms
// are covered: `v0.0.0-<ts>-<commit>` and the base-version
// `vX.Y.Z-0.<ts>-<commit>` a tagged repo produces. A trailing `+dirty` is
// tolerated; it makes the version less comparable, not more.
func isPseudoVersion(v string) bool {
	base := v
	if i := strings.IndexByte(base, '+'); i >= 0 {
		base = base[:i]
	}
	parts := strings.Split(base, "-")
	if len(parts) < 2 {
		return false
	}
	commit := parts[len(parts)-1]
	if len(commit) != 12 || !isHexLower(commit) {
		return false
	}
	ts := parts[len(parts)-2]
	if i := strings.LastIndexByte(ts, '.'); i >= 0 {
		ts = ts[i+1:]
	}
	return len(ts) == 14 && isDigits(ts)
}

func isHexLower(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// InstallCommand is the single spelling of "install this forge version",
// shared by every diagnostic that offers one.
func InstallCommand(version string) string {
	return "go install " + ModulePath + "/cmd/forge@" + version
}

// RunPinnedCommand spells "run THIS command with forge `version`", without
// installing anything: `go run <module>/cmd/forge@<version> <args>`.
//
// It is the fix to offer when the forge on PATH is not the one a project
// pins. InstallCommand REPLACES the forge on PATH, which is the wrong advice
// for a machine whose installed forge is deliberate — a developer's own dev
// build, or another project's pin — and following it is how one project's
// fix silently breaks the next. `go run` builds the pinned forge into the Go
// build cache (fetched once, reused after) and runs exactly the command that
// was refused. It works because forge's go.mod carries no `replace`.
//
// args are the forge arguments of the refused invocation (no binary name);
// each is shell-quoted only when it needs to be, so the line pastes as-is.
func RunPinnedCommand(version string, args []string) string {
	return RunBinaryCommand("go run "+ModulePath+"/cmd/forge@"+version, args)
}

// RunBinaryCommand spells the refused command run by a forge that is already on
// disk: binary (shell-quoted if it needs to be) then the arguments.
func RunBinaryCommand(binary string, args []string) string {
	var b strings.Builder
	if strings.HasPrefix(binary, "go run ") {
		b.WriteString(binary)
	} else {
		b.WriteString(shellWord(binary))
	}
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(shellWord(a))
	}
	return b.String()
}

// shellWord returns a single shell word for s: bare when it holds only
// characters no POSIX shell interprets, single-quoted otherwise.
func shellWord(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@,+%", r)) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

// BinaryBehindPin reports whether the running binary is older than the
// project's declared forge pin — the pairing whose KCL render fails while
// blaming the project.
func BinaryBehindPin(pinned, binary string) bool {
	p, b, ok := comparablePins(pinned, binary)
	if !ok {
		return false
	}
	return semver.Compare(b, p) < 0
}

// SkewDiagnosis returns the leading diagnosis for a binary older than the
// project's pin, or "" when there is no comparable skew to report.
//
// It is phrased as a runbook (expected / found / Fix) to match forge's other
// refusals, and it says explicitly that the project's KCL is not at fault —
// that sentence is the one that saves the reader from the line number KCL
// blamed.
func SkewDiagnosis(pinned, binary string) string {
	p, b, ok := comparablePins(pinned, binary)
	if !ok || semver.Compare(b, p) >= 0 {
		return ""
	}
	return fmt.Sprintf(
		"this project pins forge %s but you are running %s — an OLDER binary.\n"+
			"  forge's KCL schema module is embedded in the binary, so this render evaluated\n"+
			"  your deploy/kcl against %s's schemas. A field the project is entitled to use\n"+
			"  is simply absent, and KCL reports that against YOUR line number. It is\n"+
			"  not a mistake in your KCL.\n"+
			"    expected: a forge binary >= the project's pin (%s)\n"+
			"    found:    %s\n"+
			"  Fix: install the pinned version:\n"+
			"    %s",
		p, b, b, p, b, InstallCommand(p))
}

// PinnedForgeVersion reads forge.yaml's forge_version, walking UP from dir to
// find the project root.
//
// The walk is load-bearing, not convenience. The render seam knows a workDir,
// which for some callers is the project root and for others a directory
// beneath it. A reader that only looked in dir would go silent for exactly
// the deep-render callers, which is where this bug was hit.
//
// Returns "" for anything it cannot answer. A diagnostic must never be the
// thing that fails, so every error path here is a silent one.
func PinnedForgeVersion(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		data, err := os.ReadFile(filepath.Join(abs, "forge.yaml"))
		if err == nil {
			var meta struct {
				ForgeVersion string `yaml:"forge_version"`
			}
			if yaml.Unmarshal(data, &meta) == nil {
				return strings.TrimSpace(meta.ForgeVersion)
			}
			return ""
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}
