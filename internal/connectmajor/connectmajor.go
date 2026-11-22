// Package connectmajor decides which major of connect-go — v1
// (connectrpc.com/connect) or v2 (connectrpc.com/connect/v2) — a project's
// generated and scaffolded Go code targets, and names each major's module
// paths.
//
// # The rule
//
// A project is on v2 iff its ROOT go.mod has a direct (not `// indirect`)
// require of connectrpc.com/connect/v2. Everything else is v1: a go.mod that
// requires only v1, one that requires neither, an absent go.mod. v1 is forge's
// historical default, so no project changes major on a forge bump.
//
// Presence alone is not the signal. otelconnect v0.12 imports both majors and
// vanguard v0.5 is v2-only, so a v1 project's module graph — and its go.mod,
// after `go mod tidy` — carries connectrpc.com/connect/v2 as an indirect
// require. Only a direct require says the project's own code imports it.
//
// A go.mod that directly requires BOTH majors is mid-migration, and resolves
// to v2: protoc-gen-connect-go can emit only one major into a package (both
// write `<pkg>connect/<file>.connect.go`), and the v2 migration's flow is to
// regenerate with the v2 plugin and then port the handwritten code — the
// project has already chosen to migrate.
//
// # Why there is no forge.yaml override
//
// go.mod is where a Go project declares which connect-go it uses, and forge
// derives what it can from the project's real sources rather than keeping a
// copy in forge.yaml (internal/config: no `components:`, no `packages:`, for
// the same reason). A second declaration could disagree with the module the
// code compiles against, and a forge that generated v2 stubs into a module
// that cannot import them has produced a broken build, not a choice. Moving a
// project to v2 is `go get connectrpc.com/connect/v2` plus the import in its
// code; `forge generate` follows.
//
//forge:exclude-contract: a value type (Major) and the pure functions that decide it from go.mod; there is no collaborator to substitute
package connectmajor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// Major is a connect-go major version. The zero value is "not decided": a
// caller holding an optional Major resolves it with OrDefault (or Detect).
type Major int

const (
	// V1 is connectrpc.com/connect: generated `New<Svc>Handler(svc, opts...)
	// (path, http.Handler)`, handlers typed on connect.Request/Response.
	V1 Major = 1
	// V2 is connectrpc.com/connect/v2: generated `Register<Svc>Handler(server,
	// svc)` on a *connect.Server, bare-message ("simple") handlers.
	V2 Major = 2
)

// Module paths of the connect-go runtime, which are also the import paths of
// its root package (package name `connect` in both).
const (
	ModuleV1 = "connectrpc.com/connect"
	ModuleV2 = "connectrpc.com/connect/v2"
)

// V2ScaffoldVersion is the connect-go v2 release forge scaffolds a v2
// project against: the version its go.mod and gen/go.mod templates require,
// and the version a --buf-plugins=remote v2 project pins the BSR plugin at.
// It is held equal to forge's own go.mod requirement of ModuleV2 by
// TestV2ScaffoldVersionMatchesForgeGoMod, so a forge bump of connect-go v2
// moves the scaffold with it.
const V2ScaffoldVersion = "v2.0.2"

// Default is the major a project resolves to with no evidence: forge's
// historical (and, within forge major 1, permanent) default.
const Default = V1

// Parse reads a Major from its spelling: "v1"/"1" or "v2"/"2".
func Parse(s string) (Major, error) {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "v1", "1":
		return V1, nil
	case "v2", "2":
		return V2, nil
	}
	return 0, fmt.Errorf("unknown connect-go major %q (want v1 or v2)", s)
}

// String spells the major as "v1" or "v2" ("unset" for the zero value).
func (m Major) String() string {
	switch m {
	case V1:
		return "v1"
	case V2:
		return "v2"
	case 0:
		return "unset"
	}
	return fmt.Sprintf("Major(%d)", int(m))
}

// OrDefault returns m, or Default when m is the zero value.
func (m Major) OrDefault() Major {
	if m == 0 {
		return Default
	}
	return m
}

// IsV2 reports whether m is connect-go v2.
func (m Major) IsV2() bool { return m == V2 }

// Module is the connect-go runtime module of this major — the module whose
// version go.mod resolves, and the import path of its root package.
func (m Major) Module() string {
	if m == V2 {
		return ModuleV2
	}
	return ModuleV1
}

// RuntimeImport is the import path of the connect-go runtime package. It
// equals Module; the separate name says which use a caller means.
func (m Major) RuntimeImport() string { return m.Module() }

// PluginModule is the package `go install` builds protoc-gen-connect-go of
// this major from. Both majors name the binary protoc-gen-connect-go, so a
// cache or install keyed on the binary name alone cannot tell them apart:
// key on this (or on Module) as well.
func (m Major) PluginModule() string { return m.Module() + "/cmd/protoc-gen-connect-go" }

// VersionModule is the module whose go.mod-resolved version the plugin is
// installed at — the runtime module, since protoc-gen-connect-go ships in it.
func (m Major) VersionModule() string { return m.Module() }

// IsRuntimeImport reports whether importPath is the connect-go runtime
// package of either major, and which. The pre-rename module
// (github.com/bufbuild/connect-go) is v1's API under another path.
func IsRuntimeImport(importPath string) (Major, bool) {
	switch importPath {
	case ModuleV1, "github.com/bufbuild/connect-go":
		return V1, true
	case ModuleV2:
		return V2, true
	}
	return 0, false
}

// Detect resolves the major of the project whose root is projectDir, from
// projectDir/go.mod (see the package doc for the rule). A missing go.mod is
// Default with no error. A go.mod that cannot be read or parsed returns
// Default AND the error, so a caller that must not guess can refuse while one
// that only needs a best answer can carry on.
func Detect(projectDir string) (Major, error) {
	path := filepath.Join(projectDir, "go.mod")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default, nil
	}
	if err != nil {
		return Default, fmt.Errorf("connect-go major: %w", err)
	}
	m, err := FromGoMod(path, data)
	if err != nil {
		return Default, fmt.Errorf("connect-go major: %w", err)
	}
	return m, nil
}

// DetectOrDefault is Detect for callers that only need a best answer: an
// unreadable go.mod resolves to Default.
func DetectOrDefault(projectDir string) Major {
	m, _ := Detect(projectDir)
	return m
}

// FromGoMod applies the rule to the contents of a go.mod; path names the file
// in parse errors.
func FromGoMod(path string, data []byte) (Major, error) {
	// ParseLax: only the require block matters, and a go.mod with a
	// directive newer than this forge's x/mod must still resolve.
	f, err := modfile.ParseLax(path, data, nil)
	if err != nil {
		return Default, err
	}
	for _, r := range f.Require {
		if r.Mod.Path == ModuleV2 && !r.Indirect {
			return V2, nil
		}
	}
	return Default, nil
}
