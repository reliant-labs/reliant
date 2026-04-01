package config

import (
	"embed"
	"fmt"
	"strings"

	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	"gopkg.in/yaml.v3"
)

//go:embed recommended-skills.yaml
var recommendedSkillsFS embed.FS

// RecommendedSkillsConfig represents the structure of recommended-skills.yaml.
type RecommendedSkillsConfig struct {
	Recommended []RecommendedSkill `yaml:"recommended"`
}

// RecommendedSkill represents a single backend-curated recommended skill entry.
type RecommendedSkill struct {
	ID            string  `yaml:"id"`
	Name          string  `yaml:"name"`
	Description   string  `yaml:"description"`
	Source        string  `yaml:"source"`
	SourceSubpath *string `yaml:"sourceSubpath,omitempty"`
	Ref           *string `yaml:"ref,omitempty"`
	BundledBy     *string `yaml:"bundledBy,omitempty"`
}

// LoadRecommendedSkills loads recommended skills from the embedded YAML file.
func LoadRecommendedSkills() (*RecommendedSkillsConfig, error) {
	data, err := recommendedSkillsFS.ReadFile("recommended-skills.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to read recommended-skills.yaml: %w", err)
	}

	var cfg RecommendedSkillsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse recommended-skills.yaml: %w", err)
	}

	if cfg.Recommended == nil {
		cfg.Recommended = []RecommendedSkill{}
	}

	return &cfg, nil
}

// Validate ensures recommended skill entries are structurally valid.
func (c *RecommendedSkillsConfig) Validate() error {
	if c == nil {
		return nil
	}
	for i, rec := range c.Recommended {
		if rec.ID == "" {
			return fmt.Errorf("recommended[%d]: id is required", i)
		}
		if rec.Name == "" {
			return fmt.Errorf("recommended[%d]: name is required", i)
		}
		if err := skillcatalog.ValidateSkillCoreFields(rec.Name, rec.Description, "", nil); err != nil {
			return fmt.Errorf("recommended[%d]: %w", i, err)
		}
		if rec.Source == "" {
			return fmt.Errorf("recommended[%d]: source is required", i)
		}
		if strings.TrimSpace(rec.Source) != "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(rec.Source)), "http://") {
			return fmt.Errorf("recommended[%d]: source must not use insecure http protocol", i)
		}
		if rec.Ref == nil || strings.TrimSpace(*rec.Ref) == "" {
			return fmt.Errorf("recommended[%d]: ref is required for recommended skills", i)
		}
	}
	return nil
}
