package config

import (
	"os"
	"path/filepath"
)

// ProjectConfigFile is the fixed name of a forge project's manifest. It is
// declared here so every on-disk read spells it the same way.
const ProjectConfigFile = "forge.yaml"

// LoadProjectDir reads <projectDir>/forge.yaml and loads it through
// [LoadProject]. It is the ONLY sanctioned way to answer a forge.yaml
// question from a directory path.
//
// Callers that need one optional toggle mid-codegen used to hand-roll a
// line scan over the file rather than take this dependency, each one
// justified by an import cycle that does not exist (internal/config
// imports only internal/naming). Those scans agreed with the real parser
// only for the exact block-style spelling forge itself emits: flow style
// (`api: {rest: true}`), a nested key indented under a sibling, or an
// anchor/alias all silently read as "absent". Route through here instead
// and treat a load error as "the caller's default" — a broken forge.yaml
// has already failed the command that loaded it first.
func LoadProjectDir(projectDir string) (*ProjectConfig, error) {
	path := filepath.Join(projectDir, ProjectConfigFile)
	data, err := os.ReadFile(path) //nolint:gosec // path is a project dir + fixed filename
	if err != nil {
		return nil, err
	}
	return LoadProject(data, path)
}
