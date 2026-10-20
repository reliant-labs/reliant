// Copyright (c) 2025 Reliant Labs
package scenario

import (
	"bytes"
	"fmt"
	"io"
	"os"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"gopkg.in/yaml.v3"
)

// ScenarioFile is a wrapper format for scenario files with an array of scenarios
type ScenarioFile struct {
	APIVersion string      `yaml:"apiVersion"`
	Scenarios  []*Scenario `yaml:"scenarios"`
}

// wrapperListKey is ScenarioFile's list key: a document with it at the top
// level is the wrapper format. It is a YAML key, not the scenarios directory,
// whose layout lives in workflowref.
const wrapperListKey = "scenarios"

// scenarioDoc is one YAML document of a scenario file: a single scenario, or a
// wrapper holding a list of them.
type scenarioDoc struct {
	wrapper   bool
	scenarios []*Scenario
}

// readScenarioDocs decodes every non-empty document of data. Decoding is
// strict: a key the schema does not declare is an error naming the field and
// line. A lenient decode drops such keys, and a scenario whose assertion was
// written under the wrong key (`contains:` for `content_contains:`) then loads
// as an empty assertion and passes whatever the run did.
//
// Two decoders walk the stream in step: one into yaml.Node to see which shape
// a document has, one into the typed struct with KnownFields set. Decoding a
// Node would not carry that option, and re-encoding it would lose the line
// numbers the errors report.
func readScenarioDocs(data []byte) ([]scenarioDoc, error) {
	shape := yaml.NewDecoder(bytes.NewReader(data))
	typed := yaml.NewDecoder(bytes.NewReader(data))
	typed.KnownFields(true)

	var docs []scenarioDoc
	for {
		var node yaml.Node
		err := shape.Decode(&node)
		if err == io.EOF {
			return docs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
		}
		if len(node.Content) == 0 || node.Content[0].Kind == 0 {
			var skip interface{}
			if err := typed.Decode(&skip); err != nil && err != io.EOF {
				return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
			}
			continue // an empty document between separators
		}

		if hasTopLevelKey(node.Content[0], wrapperListKey) {
			var wrapper ScenarioFile
			if err := typed.Decode(&wrapper); err != nil {
				return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
			}
			docs = append(docs, scenarioDoc{wrapper: true, scenarios: wrapper.Scenarios})
			continue
		}

		var sc Scenario
		if err := typed.Decode(&sc); err != nil {
			return nil, fmt.Errorf("failed to parse scenario YAML: %w", err)
		}
		docs = append(docs, scenarioDoc{scenarios: []*Scenario{&sc}})
	}
}

func hasTopLevelKey(n *yaml.Node, key string) bool {
	if n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}

// ParseScenarioYAML parses scenario YAML bytes into Scenario structs.
// Supports multi-document YAML or wrapper format. A scenario without a name is
// an error rather than being skipped: a skipped scenario never runs, so its
// assertions would pass by not existing.
func ParseScenarioYAML(data []byte) ([]*Scenario, error) {
	docs, err := readScenarioDocs(data)
	if err != nil {
		return nil, err
	}
	var scenarios []*Scenario
	for _, d := range docs {
		for i, sc := range d.scenarios {
			if sc == nil || sc.Name == "" {
				return nil, fmt.Errorf("scenario %d has no name", len(scenarios)+i+1)
			}
		}
		scenarios = append(scenarios, d.scenarios...)
	}
	return scenarios, nil
}

// ParseScenarioFile parses a PROJECT scenario file: one scenario per file, as
// .reliant/workflows/<slug>/scenarios/<name>.yaml holds them
// (workflowref.ScenarioPath). A file that declares no name: takes
// defaultName, its file stem.
//
// The CLI and the app both read project scenarios with this. The app
// identifies a project scenario by its file, so a file holding several would
// silently lose all but the first there; it is an error here instead, on
// both surfaces, and for the builtin scenarios, which use the same layout.
// (ParseScenarioYAML, which accepts many, is for scenarios written inline.)
func ParseScenarioFile(data []byte, defaultName string) (*Scenario, error) {
	docs, err := readScenarioDocs(data)
	if err != nil {
		return nil, err
	}
	var scenarios []*Scenario
	for _, d := range docs {
		if d.wrapper && len(d.scenarios) > 0 {
			return nil, fmt.Errorf("holds a list of %d scenarios; a project scenario file holds one — split it into one file per scenario", len(d.scenarios))
		}
		scenarios = append(scenarios, d.scenarios...)
	}
	switch len(scenarios) {
	case 0:
		return nil, fmt.Errorf("holds no scenario")
	case 1:
	default:
		return nil, fmt.Errorf("holds %d scenarios; a project scenario file holds one — split it into one file per scenario", len(scenarios))
	}
	sc := scenarios[0]
	if sc.Name == "" {
		sc.Name = defaultName
	}
	return sc, nil
}

// LoadWorkflowFromFile loads a workflow from a YAML file on disk.
func LoadWorkflowFromFile(path string) (*reliantv1.Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow file %s: %w", path, err)
	}

	wf, err := v2.ParseWorkflowProtoBytes(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse workflow %s: %w", path, err)
	}

	return wf, nil
}
