package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadRecommendedSkills_ParsesEmbeddedConfig(t *testing.T) {
	cfg, err := LoadRecommendedSkills()
	if err != nil {
		t.Fatalf("failed to load recommended skills: %v", err)
	}

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Recommended == nil {
		t.Fatal("expected recommended list to be non-nil")
	}
	if len(cfg.Recommended) != 7 {
		t.Fatalf("expected 7 recommended skills, got %d entries", len(cfg.Recommended))
	}

	ids := make(map[string]struct{}, len(cfg.Recommended))
	for _, rec := range cfg.Recommended {
		ids[rec.ID] = struct{}{}
	}
	require.Contains(t, ids, "playwright-cli")
	require.Contains(t, ids, "frontend-design")
	require.Contains(t, ids, "docx")
	require.Contains(t, ids, "pdf")
	require.Contains(t, ids, "xlsx")
	require.Contains(t, ids, "pptx")
	require.Contains(t, ids, "mcp-builder")

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected embedded config to validate, got: %v", err)
	}
}

func TestRecommendedSkillsConfigValidate_RequiresFields(t *testing.T) {
	cfg := &RecommendedSkillsConfig{
		Recommended: []RecommendedSkill{{}},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for empty entry")
	}
}

func TestRecommendedSkillsConfigValidate_NameConstraints(t *testing.T) {
	ref := "main"
	cfg := &RecommendedSkillsConfig{
		Recommended: []RecommendedSkill{{
			ID:          "bad",
			Name:        "Bad_Name",
			Description: "desc",
			Source:      "https://example.com/repo.git",
			Ref:         &ref,
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid name")
	}
}

func TestRecommendedSkillsConfigValidate_RequiresRef(t *testing.T) {
	cfg := &RecommendedSkillsConfig{
		Recommended: []RecommendedSkill{{
			ID:          "good-skill",
			Name:        "good-skill",
			Description: "desc",
			Source:      "https://example.com/repo.git",
		}},
	}

	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "ref is required")
}

func TestRecommendedSkillsConfigValidate_AcceptsNamedRef(t *testing.T) {
	ref := "main"
	cfg := &RecommendedSkillsConfig{
		Recommended: []RecommendedSkill{{
			ID:          "good-skill",
			Name:        "good-skill",
			Description: "desc",
			Source:      "https://example.com/repo.git",
			Ref:         &ref,
		}},
	}

	require.NoError(t, cfg.Validate())
}

func TestRecommendedSkillsConfigValidate_RejectsInsecureSourceProtocol(t *testing.T) {
	ref := "main"
	cfg := &RecommendedSkillsConfig{
		Recommended: []RecommendedSkill{{
			ID:          "good-skill",
			Name:        "good-skill",
			Description: "desc",
			Source:      "http://example.com/repo.git",
			Ref:         &ref,
		}},
	}

	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not use insecure http protocol")
}

func TestRecommendedSkillsConfigValidate_AcceptsHTTPSSourceWithRef(t *testing.T) {
	ref := "release-2026"
	cfg := &RecommendedSkillsConfig{
		Recommended: []RecommendedSkill{{
			ID:          "good-skill",
			Name:        "good-skill",
			Description: "desc",
			Source:      "https://example.com/repo.git",
			Ref:         &ref,
		}},
	}

	require.NoError(t, cfg.Validate())
}
