package wfyaml

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// reviewNodeType is model.NodeTypeReview (this package does not import model).
const reviewNodeType = "review"

// reviewEngineKeys are keys that would reconfigure what the engine owns on a
// review node: its tools, its session and the workflow it runs. Refused by
// name, so the author learns why rather than reading "unknown key".
var reviewEngineKeys = []string{
	"tools", "tools_config", "preloaded_tools", "loadable_tools", "spawn", "spawn_presets",
	"permission", "presets", "thread", "ref", "inline", "args", "passthrough", "system_prompt",
}

func refuseReviewEngineKeys(yamlMap map[string]*yaml.Node) error {
	for _, key := range reviewEngineKeys {
		if _, ok := yamlMap[key]; ok {
			return fmt.Errorf("a review node cannot set %q: its tools, its session and the agent it runs are fixed by the engine (a reviewer reads, in a fresh session, and only returns a verdict)", key)
		}
	}
	return nil
}
