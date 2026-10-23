//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// configLinkMain imports BOTH projects' generated pkg/config and names each
// root config message. Linking them runs every generated .pb.go's init, which
// registers its proto file in protobuf's one process-wide registry; on the old
// shared layout (config/v1/config.proto, package config.v1) the second
// registration panicked — "file config/v1/config.proto is already registered"
// — before main ran.
const configLinkMain = `package main

import (
	"fmt"

	alphaconfig "github.com/test/alpha/pkg/config"
	betaconfig "github.com/test/beta-svc/pkg/config"
)

func main() {
	a := (&alphaconfig.Config{}).ProtoReflect().Descriptor()
	b := (&betaconfig.Config{}).ProtoReflect().Descriptor()
	fmt.Println(a.ParentFile().Path(), a.FullName())
	fmt.Println(b.ParentFile().Path(), b.FullName())
}
`

// TestE2ETwoProjectsConfigLinkIntoOneBinary is the regression for the
// cross-project registration panic: two forge projects' generated config
// packages, linked into one binary, must start — the use roots_gen.go
// documents (another repo imports a project's pkg/config at a pin and checks a
// rendered env with Roots()/ValidateRootEnv). Each project's config proto is
// at proto/<project>/config/v1 in package <project>.config.v1, so the two
// register disjoint paths and names.
func TestE2ETwoProjectsConfigLinkIntoOneBinary(t *testing.T) {
	t.Parallel() // independent projects in their own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	for _, p := range []struct{ name, mod string }{
		{"alpha", "github.com/test/alpha"},
		{"beta-svc", "github.com/test/beta-svc"},
	} {
		runCmd(t, dir, forgeBin, "project", "new", p.name, "--mod", p.mod)
		projectDir := filepath.Join(dir, p.name)
		addCorpusForgePkgReplace(t, projectDir)
		runCmd(t, projectDir, forgeBin, "generate")
	}

	// alpha links beta: its module and beta's gen module, by directory.
	alpha := filepath.Join(dir, "alpha")
	addModuleLines(t, filepath.Join(alpha, "go.mod"),
		"require github.com/test/beta-svc v0.0.0",
		"require github.com/test/beta-svc/gen v0.0.0",
		"replace github.com/test/beta-svc => ../beta-svc",
		"replace github.com/test/beta-svc/gen => ../beta-svc/gen",
	)
	writeFileE2E(t, filepath.Join(alpha, "cmd", "configlink", "main.go"), configLinkMain)
	runCmd(t, alpha, "go", "mod", "tidy")
	bin := filepath.Join(alpha, "configlink-bin")
	runCmd(t, alpha, "go", "build", "-o", bin, "./cmd/configlink")

	out := runCmdOutput(t, alpha, bin)
	for _, want := range []string{
		"alpha/config/v1/config.proto alpha.config.v1.AppConfig",
		"beta_svc/config/v1/config.proto beta_svc.config.v1.AppConfig",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the linked binary printed\n%s\nwant a line %q", out, want)
		}
	}
}
