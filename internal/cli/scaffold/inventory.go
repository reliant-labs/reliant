package scaffold

import (
	"path/filepath"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// readProject is the ONE project read every `forge scaffold` verb goes
// through. It returns two SEPARATE things, because they come from two
// different places and neither is derivable from the other:
//
//   - the project-global config, parsed from forge.yaml;
//   - the component inventory, read from the code that declares it — the
//     proto descriptor for servers, plus the internal/workers/,
//     internal/operators/ and cmd/ trees the add verbs themselves write.
//
// Two return values rather than one bundle, on purpose: a verb that wants to
// know what the project already contains has to name the inventory to get at
// it. There is no config field it could read and quietly get an empty answer
// from, and no way to hold a config and believe it carries the components.
//
// The inventory is read ONCE here and passed down through the add spine, so
// a single verb never re-walks the tree.
func readProject(root, ctxLabel string) (*config.ProjectConfig, codegen.Inventory, error) {
	configPath := filepath.Join(root, "forge.yaml")
	cfg, err := generator.ReadProjectConfig(configPath)
	if err != nil {
		return nil, nil, cliutil.WrapUserErr(ctxLabel, "read project config", configPath,
			"verify forge.yaml is valid YAML", err)
	}
	return cfg, codegen.DiscoverProjectComponents(root, binaryName(cfg, root)), nil
}
