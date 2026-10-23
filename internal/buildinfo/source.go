package buildinfo

import (
	"runtime/debug"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// Source is the forge SOURCE this binary was compiled from, as far as its
// build recorded it. The version-skew check compares it with the checkout a
// project bridges to: generated code comes from this binary, the library it
// compiles against comes from that checkout, and the two must be one commit.
type Source struct {
	// Revision is the forge commit (full or 12-char) the build recorded, or
	// "" when it recorded none.
	Revision string
	// Modified reports the binary was built from a dirty forge tree.
	Modified bool
	// Tag is a release tag the build names but whose commit is not in build
	// info (`go install …/cmd/forge@v0.1.44`). A caller with a checkout can
	// resolve it there.
	Tag string
	// Embedded reports forge is compiled into a host binary (`reliant forge`).
	Embedded bool
}

// ForgeSource reports the forge source this binary was compiled from.
func ForgeSource() Source {
	mu.RLock()
	stamped := gitCommit
	mu.RUnlock()
	return sourceFrom(rawBuildInfo(), stamped)
}

// sourceFrom is the pure decision behind ForgeSource.
//
// Which facts describe FORGE depends on how forge was linked:
//
//   - standalone (forge is the main module): vcs.revision / vcs.modified are
//     forge's own. Without them (a `go install …@version` build), the module
//     version names the commit — a pseudo-version carries it, a tag must be
//     resolved by the caller. A release's ldflags commit stamp is the last
//     resort.
//   - embedded at a real version (the host required forge from a proxy): the
//     dependency's version names the commit the same way.
//   - embedded through a workspace or a directory replace: forge is
//     "(devel)" and the vcs.* settings are the HOST's. Nothing in the binary
//     names forge's commit, and Revision stays "" — callers must not
//     substitute the host's.
func sourceFrom(info *debug.BuildInfo, stampedCommit string) Source {
	var s Source
	if info == nil {
		return s
	}
	if info.Main.Path == forgeModulePath {
		for _, kv := range info.Settings {
			switch kv.Key {
			case "vcs.revision":
				s.Revision = kv.Value
			case "vcs.modified":
				s.Modified = kv.Value == "true"
			}
		}
		if s.Revision == "" {
			s.Revision, s.Tag = commitOfVersion(info.Main.Version)
		}
		if s.Revision == "" && s.Tag == "" {
			if c := strings.TrimSpace(stampedCommit); c != "" && c != "unknown" {
				s.Revision = c
			}
		}
		return s
	}
	dep, embedded := forgeModuleDep(info)
	if !embedded {
		return s
	}
	s.Embedded = true
	if dep.Replace != nil {
		if dep.Replace.Version == "" {
			return s // a directory: the bytes are whatever that tree held
		}
		s.Revision, s.Tag = commitOfVersion(dep.Replace.Version)
		return s
	}
	s.Revision, s.Tag = commitOfVersion(dep.Version)
	return s
}

// commitOfVersion extracts what a module version says about its commit: a
// pseudo-version's revision, or a release tag to be resolved elsewhere.
// "(devel)", "" and build-metadata-only shapes say nothing.
func commitOfVersion(v string) (revision, tag string) {
	v = strings.TrimSpace(v)
	if v == "" || v == "(devel)" || !semver.IsValid(v) {
		return "", ""
	}
	if module.IsPseudoVersion(v) {
		if rev, err := module.PseudoVersionRev(v); err == nil {
			return rev, ""
		}
		return "", ""
	}
	if semver.Build(v) != "" {
		return "", "" // `+dirty` / `+dev` describe a working tree, not a tag
	}
	return "", v
}

// SourceRoot is the local forge checkout this binary was compiled from — the
// ldflags stamp when a contributor build supplied one, otherwise recovered
// from the binary's compiled file paths — or "" when it has none (a release
// built with -trimpath, or a binary copied off the machine that built it).
func SourceRoot() string { return forgeSourceRoot() }
