// Package toolpin answers one question for every forge surface that runs buf:
// which buf does THIS project pin?
//
// buf had two pins in one project. forge pinned it (templates.BufVersion, what
// `forge ci gate` holds PATH to and the scaffolded ci.yml installs), and a
// project that needed another release kept its own copy of the decision — a
// Makefile BUF_VERSION smuggled into forge through $BUF and PATH (reliant: buf
// 1.57.0 against forge's 1.50.0). The two answers drifted, and which buf
// generated a file depended on whether forge or the Makefile started it.
//
// Now the project's go.mod is the one place it is declared, exactly as it is
// for protoc-gen-go and protoc-gen-connect-go: a project that pins buf says so
// with a tool directive,
//
//	go get -tool github.com/bufbuild/buf/cmd/buf@v1.57.0
//
// and every forge surface — `forge generate`, `forge ci gate` (verify-generated
// and buf-lint), `forge tools install buf`, `forge tools pins buf`, and the
// ci.yml forge scaffolds — runs that release. A project that declares none runs
// forge's pin. Dependabot moves the go.mod line like any other.
package toolpin

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/templates"
)

// BufModule is buf's Go module; a go.mod that requires it (as a tool
// directive does) pins the buf the project runs.
const BufModule = "github.com/bufbuild/buf"

// Source says where a resolved buf version came from.
type Source string

const (
	// FromGoMod is the project's go.mod requiring BufModule.
	FromGoMod Source = "go.mod"
	// FromForge is forge's own pin, for a project that declares none.
	FromForge Source = "forge"
)

// Buf is the buf a project runs.
type Buf struct {
	// Version is a Go module version, with its leading v ("v1.57.0").
	Version string
	Source  Source
}

// Bare is Version without its leading v ("1.57.0"), the spelling
// bufbuild/buf-setup-action and `buf --version` use.
func (b Buf) Bare() string { return strings.TrimPrefix(b.Version, "v") }

// Declared reports whether the project pins buf itself.
func (b Buf) Declared() bool { return b.Source == FromGoMod }

var (
	mu    sync.Mutex
	cache = map[string]Buf{}
)

// BufFor returns the buf projectDir pins: go.mod's BufModule when the module
// graph has it, forge's pin otherwise. projectDir "" (no project) is forge's
// pin. Memoised per directory for the life of the process.
func BufFor(projectDir string) Buf {
	if projectDir == "" {
		return ForgeBuf()
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	mu.Lock()
	defer mu.Unlock()
	if b, ok := cache[abs]; ok {
		return b
	}
	b := ForgeBuf()
	if v := goModVersion(abs); v != "" {
		b = Buf{Version: v, Source: FromGoMod}
	}
	cache[abs] = b
	return b
}

// ForgeBuf is forge's own pin.
func ForgeBuf() Buf { return Buf{Version: "v" + templates.BufVersion, Source: FromForge} }

// goModVersion is the BufModule version dir's module graph selects, "" when
// the graph has none (or dir has no go.mod). GOWORK=off: the committed
// go.mod is the declaration, not a developer's workspace; goexec.Env keeps a
// caller's -mod=mod from letting the lookup write go.sum.
var goModVersion = func(dir string) string {
	cmd := exec.CommandContext(context.Background(), "go", "list", "-m", "-f", "{{.Version}}", BufModule)
	cmd.Dir = dir
	cmd.Env = goexec.Env("GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
