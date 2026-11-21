package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/configproto"
)

// HasConfigProtos reports whether the project's config directory
// (internal/configproto: proto/<project>/config/v1) holds a .proto file —
// the input the config loader generates pkg/config from. A config directory
// with no proto in it (a CLI scaffold leaves an empty one) is not one:
// generating the loader from forge's default fields there writes a
// pkg/config importing a config package nothing will ever generate.
//
// A project still on the legacy proto/<project>/config/v1 layout reports false here;
// `forge generate` refuses it before this is consulted (see
// configproto.LegacyFiles).
func HasConfigProtos(projectDir string) bool {
	return configproto.ForProject(projectDir).HasProtos(projectDir)
}

// configTypeRE matches a package-level `type Config` declaration.
var configTypeRE = regexp.MustCompile(`(?m)^type\s+Config\b`)

// ProjectHasTypedConfig reports whether the project has the typed config
// a scaffolded component's `Deps.Config *config.Config` names: config
// protos the loader turns into pkg/config, or a pkg/config that already
// declares Config (a CLI's stub, a hand-written one).
//
// A project adopted in place often has neither — its configuration lives
// in its own package. A component scaffolded there takes no Config:
// importing <module>/pkg/config would not compile, and a placeholder
// package configuring nothing is an empty default every caller would have
// to construct and pass. The project adds the field when it has config to
// give.
func ProjectHasTypedConfig(projectDir string) bool {
	if HasConfigProtos(projectDir) {
		return true
	}
	dir := filepath.Join(projectDir, "pkg", "config")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if data, rerr := os.ReadFile(filepath.Join(dir, name)); rerr == nil && configTypeRE.Match(data) {
			return true
		}
	}
	return false
}
