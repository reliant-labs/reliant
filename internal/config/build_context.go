package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// BuildContext is either a local path / BuildKit reference (a scalar), or a
// GitSource pin (a mapping). These alternatives cannot silently override one
// another. Resolution belongs to the CLI; loading config never fetches code.
type BuildContext struct {
	Path   string
	Source *GitSource
	// SourceDir is the resolved invocation-local directory, never serialized.
	SourceDir string
}

// Validate rejects empty contexts, conflicting alternatives and malformed pins.
func (c BuildContext) Validate() error {
	if c.Source == nil {
		if c.Path == "" {
			return fmt.Errorf("build context must be a non-empty path/reference or a GitSource")
		}
		return nil
	}
	if c.Path != "" {
		return fmt.Errorf("build context path and GitSource are mutually exclusive")
	}
	return c.Source.Validate()
}

// Validate rejects incomplete pins and subdirectories escaping the repository.
func (s GitSource) Validate() error {
	if s.Repo == "" || s.Ref == "" {
		return fmt.Errorf("GitSource.repo and GitSource.ref are required")
	}
	// Use slash semantics on every platform: subdir is a repository path.
	for _, part := range strings.Split(strings.ReplaceAll(s.Subdir, "\\", "/"), "/") {
		if part == ".." {
			return fmt.Errorf("GitSource.subdir must not escape the repository root")
		}
	}
	if strings.HasPrefix(s.Subdir, "/") || strings.Contains(s.Subdir, ":") || strings.HasPrefix(s.Subdir, "\\") {
		return fmt.Errorf("GitSource.subdir must be relative to the repository root")
	}
	return nil
}

// UnmarshalYAML decodes a string reference or a strict GitSource mapping.
func (c *BuildContext) UnmarshalYAML(node *yaml.Node) error {
	*c = BuildContext{}
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return fmt.Errorf("build context must be a path/reference string or GitSource mapping")
		}
		c.Path = node.Value
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			switch node.Content[i].Value {
			case "repo", "ref", "subdir":
			default:
				return fmt.Errorf("unknown GitSource key %q", node.Content[i].Value)
			}
		}
		c.Source = &GitSource{}
		if err := node.Decode(c.Source); err != nil {
			return err
		}
	default:
		return fmt.Errorf("build context must be a path/reference string or GitSource mapping")
	}
	return c.Validate()
}

// MarshalYAML preserves the declaration, never the resolved local directory.
func (c BuildContext) MarshalYAML() (any, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Source != nil {
		return c.Source, nil
	}
	return c.Path, nil
}

// UnmarshalJSON decodes the same string-or-pin union emitted by KCL.
func (c *BuildContext) UnmarshalJSON(data []byte) error {
	*c = BuildContext{}
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &c.Path); err != nil {
			return err
		}
	} else {
		var source GitSource
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&source); err != nil {
			return err
		}
		c.Source = &source
	}
	return c.Validate()
}

// MarshalJSON preserves the pin or path instead of serializing resolver state.
func (c BuildContext) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Source != nil {
		return json.Marshal(c.Source)
	}
	return json.Marshal(c.Path)
}
