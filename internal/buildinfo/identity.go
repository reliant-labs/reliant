package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
)

// Forge runs two ways on one machine, and they can be DIFFERENT BUILDS:
//
//  1. standalone — `go install ./cmd/forge`. forge is the MAIN module.
//  2. embedded   — another binary (reliant) imports
//     github.com/reliant-labs/forge/cli and mounts NewRootCmd() as a
//     subcommand. forge is then a DEP of that binary, usually behind a
//     local `replace` to a sibling checkout, and only changes when the
//     HOST binary is rebuilt.
//
// Install one and not the other and a single PATH carries two forge builds
// that can disagree about the same project tree — most sharply at the
// version compatibility check in internal/cli/generate_pkg_compat.go, which
// one build passes and the other fails. Nothing in that failure used to say
// "a different forge build generated this tree."
//

// Build is the identity that makes the two distinguishable. It is derived
// from the binary's own runtime/debug build info, so it works for anything
// `go install`/`go build` produced and needs no build-system change; ldflags
// stamps, when present, take precedence.
type Build struct {
	// Version identifies FORGE, never the host binary. On an embedded
	// build this is the version of the forge module dependency — reading
	// the main module there would report the EMBEDDER's version, which is
	// the bug that stamped `forge_version: v1.5.1-...` (a reliant version)
	// into a scaffolded project's forge.yaml.
	Version string
	// Commit is the VCS revision the binary was built from.
	Commit string
	// BuildDate is the ldflags-stamped build date, if any.
	BuildDate string
	// Embedded reports whether forge is compiled into a host binary
	// rather than running as its own executable.
	Embedded bool
	// Embedder / EmbedderVersion / EmbedderCommit name the host binary on
	// an embedded build ("github.com/reliant-labs/reliant", "v1.5.1-...").
	// The host's commit matters because an embedded forge only changes
	// when the HOST is rebuilt — it is what pins which forge source ran.
	Embedder        string
	EmbedderVersion string
	EmbedderCommit  string
	// ReplacedBy is the local `replace` target the forge module resolved
	// through, when there is one ("../forge"). A local replace is exactly
	// the skew that makes two builds differ while reporting one version.
	ReplacedBy string
}

// applyEmbeddedForgeIdentity fills in the embedded-build fields: forge is a
// DEPENDENCY of some host binary rather than the main module.
//
// It reports the REQUIRED forge version rather than a replacement's "(devel)"
// placeholder, and names the replace target separately — together those say
// which build this actually is. A go.work setup has no explicit replace and
// reads "(devel)" directly, where the VERSION-file floor is all there is to
// fall back on.
func applyEmbeddedForgeIdentity(b *Build, info *debug.BuildInfo) {
	for _, dep := range info.Deps {
		if dep == nil || dep.Path != forgeModulePath {
			continue
		}
		b.Embedded = true
		b.Embedder = info.Main.Path
		b.EmbedderVersion = info.Main.Version
		b.Version = dep.Version
		if b.Version == "" || b.Version == "(devel)" {
			b.Version = versionFloor("")
		}
		if dep.Replace != nil {
			b.ReplacedBy = dep.Replace.Path
		}
		return
	}
}

// buildFrom derives forge's identity from build info, preferring the ldflags
// stamps when a release build supplied them. Pure, so the standalone and
// embedded shapes can both be unit-tested without controlling the ambient
// binary (which under `go test` is always the test binary).
func buildFrom(info *debug.BuildInfo, ldflagsVersion, ldflagsCommit string) Build {
	var b Build

	if info != nil {
		if info.Main.Path == forgeModulePath {
			// forge IS the main module: standalone.
			b.Version = info.Main.Version
			if b.Version == "" || b.Version == "(devel)" {
				b.Version = versionFloor("")
			}
		} else {
			applyEmbeddedForgeIdentity(&b, info)
		}

		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				// Likewise the main module's revision: forge's own commit
				// when standalone, the host's when embedded (recorded as
				// EmbedderCommit, since it is still what pins which forge
				// source that host binary compiled in).
				if b.Embedded {
					b.EmbedderCommit = s.Value
				} else {
					b.Commit = s.Value
				}
			case "vcs.modified":
				// vcs.* describes the MAIN module's repository. On a
				// standalone build that is forge's own tree, so a dirty
				// marker belongs on forge's version. On an embedded build
				// it is the HOST's tree — forge's dirtiness is unknowable
				// from here, and claiming it would misattribute.
				if s.Value == "true" && !b.Embedded {
					b.Version = markDirty(b.Version)
				}
			}
		}
	}

	// ldflags stamps win: a release build states its identity explicitly.
	if v := strings.TrimSpace(ldflagsVersion); v != "" && v != "dev" {
		b.Version = v
	}
	if c := strings.TrimSpace(ldflagsCommit); c != "" && c != "unknown" {
		b.Commit = c
	}

	// An empty identity is a defect on its own — it is what
	// `reliant forge --version` printed. Never return one.
	if strings.TrimSpace(b.Version) == "" {
		if b.Commit != "" {
			b.Version = "dev"
		} else {
			b.Version = "dev (unknown build)"
		}
	}
	return b
}

// markDirty appends the +dirty marker a modified working tree implies,
// unless the version already carries build metadata saying so.
func markDirty(v string) string {
	if v == "" || strings.Contains(v, "+") {
		return v
	}
	return v + "+dirty"
}

// String renders the identity as one line, omitting clauses it has no value
// for rather than padding them with empty parentheses. It carries no "forge"
// prefix: cobra's version template already renders "<name> version <this>",
// and the `version` subcommand adds its own label.
func (b Build) String() string {
	var sb strings.Builder
	sb.WriteString(b.Version)

	var parts []string
	if c := shortCommit(b.Commit); c != "" {
		parts = append(parts, "commit "+c)
	}
	if b.BuildDate != "" {
		parts = append(parts, "built "+b.BuildDate)
	}
	if b.Embedded {
		host := b.Embedder
		if host == "" {
			host = "another binary"
		}
		if b.EmbedderVersion != "" {
			host += " " + b.EmbedderVersion
		}
		if c := shortCommit(b.EmbedderCommit); c != "" {
			host += " @" + c
		}
		parts = append(parts, "embedded in "+host)
	}
	if b.ReplacedBy != "" {
		parts = append(parts, "forge => "+b.ReplacedBy)
	}
	if len(parts) > 0 {
		sb.WriteString(" (")
		sb.WriteString(strings.Join(parts, ", "))
		sb.WriteString(")")
	}
	return sb.String()
}

// Origin names how forge is being invoked, for error messages that must tell
// a user WHICH forge ran.
func (b Build) Origin() string {
	if b.Embedded {
		host := b.Embedder
		if i := strings.LastIndex(host, "/"); i >= 0 {
			host = host[i+1:]
		}
		if host == "" {
			return "embedded forge"
		}
		return host + " forge (embedded)"
	}
	return "forge (standalone binary)"
}

func shortCommit(c string) string {
	c = strings.TrimSpace(c)
	if c == "" || c == "unknown" {
		return ""
	}
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

var (
	infoOnce  sync.Once
	cachedRaw *debug.BuildInfo

	buildDateMu sync.RWMutex
	buildDate   string
)

// SetBuildDate records the ldflags-stamped build date, which has no
// runtime/debug equivalent. Safe to call with "" (the embedded path, where
// nothing stamps one) — the clause is then omitted rather than rendered
// empty.
func SetBuildDate(d string) {
	buildDateMu.Lock()
	defer buildDateMu.Unlock()
	buildDate = strings.TrimSpace(d)
}

// rawBuildInfo reads (and caches) this binary's build info. The read is
// cached because it is fixed for the process's lifetime; the DERIVED
// identity is not cached, so a late Set/SetVersion from main() is still
// reflected.
func rawBuildInfo() *debug.BuildInfo {
	infoOnce.Do(func() {
		if info, ok := debug.ReadBuildInfo(); ok {
			cachedRaw = info
		}
	})
	return cachedRaw
}

// Current returns this binary's forge identity, derived from its own build
// info plus whatever the entrypoint stamped via Set/SetVersion.
func Current() Build {
	mu.RLock()
	v, c := version, gitCommit
	mu.RUnlock()

	b := buildFrom(rawBuildInfo(), v, c)

	buildDateMu.RLock()
	b.BuildDate = buildDate
	buildDateMu.RUnlock()
	if b.BuildDate == "unknown" {
		b.BuildDate = ""
	}
	return b
}

// Identity is the one-line build identity, the string `--version` prints and
// that failure runbooks quote to name which forge ran.
func Identity() string { return Current().String() }

// Describe renders the multi-line identity block used when a failure needs to
// spell out which forge is running and what library it resolved.
func Describe() string {
	b := Current()
	var sb strings.Builder
	fmt.Fprintf(&sb, "  forge build:    %s\n", b.Version)
	if c := shortCommit(b.Commit); c != "" {
		fmt.Fprintf(&sb, "  commit:         %s\n", c)
	}
	fmt.Fprintf(&sb, "  invoked as:     %s\n", b.Origin())
	if b.Embedded && b.Embedder != "" {
		host := b.Embedder
		if b.EmbedderVersion != "" {
			host += " " + b.EmbedderVersion
		}
		if c := shortCommit(b.EmbedderCommit); c != "" {
			host += " @" + c
		}
		fmt.Fprintf(&sb, "  embedded in:    %s\n", host)
	}
	if b.ReplacedBy != "" {
		fmt.Fprintf(&sb, "  forge module:   replaced by %s\n", b.ReplacedBy)
	}
	return sb.String()
}
