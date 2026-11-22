package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// GitConfig is the `git:` block: what forge may change in the clone's own git
// setup on its own.
type GitConfig struct {
	// MergeDriver says who wires forge's merge driver for generated files
	// into the clone's git config (merge.forge-generated.* and the routes in
	// info/attributes, which every linked worktree of the clone shares):
	//
	//	manual  only `forge merge-driver install` does. A writing command
	//	        (generate, project upgrade, scaffold, ...) changes no git
	//	        config; once per checkout and forge version it says when the
	//	        clone's driver is missing or unusable, and how to install it.
	//	auto    every writing command wires and repairs it, as forge did
	//	        before v1.8 (forge#2232).
	//	unset   auto in a clone that already has forge's driver (wired by an
	//	        earlier forge, or by `forge merge-driver install`), so a
	//	        clone that relied on writing commands keeping it working
	//	        still does; manual in a clone that has none. Set `manual` to
	//	        stop writing commands touching a clone that has one.
	//
	//	git:
	//	  merge_driver: auto
	MergeDriver string `yaml:"merge_driver,omitempty"`
}

// The values of git.merge_driver.
const (
	MergeDriverManual = "manual"
	MergeDriverAuto   = "auto"
)

// AutoMergeDriver reports whether forge.yaml says `auto`: writing commands
// wire the merge driver in every clone.
func (g GitConfig) AutoMergeDriver() bool {
	return strings.TrimSpace(g.MergeDriver) == MergeDriverAuto
}

// ManualMergeDriver reports whether forge.yaml says `manual`: writing
// commands never wire the merge driver, even in a clone that has one.
func (g GitConfig) ManualMergeDriver() bool {
	return strings.TrimSpace(g.MergeDriver) == MergeDriverManual
}

// ReadGitConfig reads only the `git:` block of the forge.yaml in projectDir.
// It is what runs in front of every writing command, so it does not load the
// whole project; a missing or unreadable file is the zero value, and an
// invalid one is left to the full loader to report.
func ReadGitConfig(projectDir string) GitConfig {
	data, err := os.ReadFile(filepath.Join(projectDir, "forge.yaml"))
	if err != nil {
		return GitConfig{}
	}
	var doc struct {
		Git GitConfig `yaml:"git"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return GitConfig{}
	}
	return doc.Git
}

func validateGitConfig(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	switch v := strings.TrimSpace(cfg.Git.MergeDriver); v {
	case "", MergeDriverManual, MergeDriverAuto:
		return nil
	default:
		line, col := findNodePos(root, []string{"git", "merge_driver"})
		return []validationIssue{{line: line, column: col,
			msg: fmt.Sprintf("git.merge_driver %q is not %q or %q", v, MergeDriverManual, MergeDriverAuto),
			fix: "`manual`: only `forge merge-driver install` wires the driver; `auto`: every writing command does; unset: `auto` in a clone that already has forge's driver, `manual` otherwise."}}
	}
}
