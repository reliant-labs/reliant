// Copyright (c) 2025 Reliant Labs
package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestModelSelector_UnmarshalJSON(t *testing.T) {
	t.Run("string format converts to ID", func(t *testing.T) {
		var m ModelSelector
		err := json.Unmarshal([]byte(`"claude-4.5-sonnet"`), &m)
		require.NoError(t, err)
		assert.Equal(t, "claude-4.5-sonnet", m.ID)
		assert.Empty(t, m.Tags)
		assert.Empty(t, m.Providers)
	})

	t.Run("string format trims whitespace", func(t *testing.T) {
		var m ModelSelector
		err := json.Unmarshal([]byte(`"  claude-4  "`), &m)
		require.NoError(t, err)
		assert.Equal(t, "claude-4", m.ID)
	})

	t.Run("struct format with id", func(t *testing.T) {
		var m ModelSelector
		err := json.Unmarshal([]byte(`{"id":"gpt-4"}`), &m)
		require.NoError(t, err)
		assert.Equal(t, "gpt-4", m.ID)
	})

	t.Run("struct format with tags", func(t *testing.T) {
		var m ModelSelector
		err := json.Unmarshal([]byte(`{"tags":["fast","cheap"]}`), &m)
		require.NoError(t, err)
		assert.Equal(t, []string{"fast", "cheap"}, m.Tags)
	})

	t.Run("struct format with providers", func(t *testing.T) {
		var m ModelSelector
		err := json.Unmarshal([]byte(`{"tags":["flagship"],"providers":["anthropic","openrouter"]}`), &m)
		require.NoError(t, err)
		assert.Equal(t, []string{"flagship"}, m.Tags)
		assert.Equal(t, []string{"anthropic", "openrouter"}, m.Providers)
	})

	t.Run("pointer field in struct with object format", func(t *testing.T) {
		type TestStruct struct {
			Model *ModelSelector `json:"model"`
		}
		var ts TestStruct
		err := json.Unmarshal([]byte(`{"model":{"id":"mock"}}`), &ts)
		require.NoError(t, err)
		require.NotNil(t, ts.Model)
		assert.Equal(t, "mock", ts.Model.ID)
	})

	t.Run("pointer field in struct with string format", func(t *testing.T) {
		type TestStruct struct {
			Model *ModelSelector `json:"model"`
		}
		var ts TestStruct
		err := json.Unmarshal([]byte(`{"model":"mock"}`), &ts)
		require.NoError(t, err)
		require.NotNil(t, ts.Model)
		assert.Equal(t, "mock", ts.Model.ID)
	})

	t.Run("null pointer", func(t *testing.T) {
		type TestStruct struct {
			Model *ModelSelector `json:"model"`
		}
		var ts TestStruct
		err := json.Unmarshal([]byte(`{}`), &ts)
		require.NoError(t, err)
		assert.Nil(t, ts.Model)
	})

	t.Run("explicit null", func(t *testing.T) {
		type TestStruct struct {
			Model *ModelSelector `json:"model"`
		}
		var ts TestStruct
		err := json.Unmarshal([]byte(`{"model":null}`), &ts)
		require.NoError(t, err)
		assert.Nil(t, ts.Model)
	})
}

func TestModelSelector_MarshalJSON(t *testing.T) {
	t.Run("ID only outputs string", func(t *testing.T) {
		m := ModelSelector{ID: "claude-4"}
		data, err := json.Marshal(m)
		require.NoError(t, err)
		assert.Equal(t, `"claude-4"`, string(data))
	})

	t.Run("tags outputs object", func(t *testing.T) {
		m := ModelSelector{Tags: []string{"fast"}}
		data, err := json.Marshal(m)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"tags"`)
		assert.Contains(t, string(data), `["fast"]`)
	})

	t.Run("ID with providers outputs object", func(t *testing.T) {
		m := ModelSelector{ID: "claude-4", Providers: []string{"anthropic"}}
		data, err := json.Marshal(m)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"id"`)
		assert.Contains(t, string(data), `"providers"`)
	})

	t.Run("round trip preserves data", func(t *testing.T) {
		original := ModelSelector{Tags: []string{"flagship", "reasoning"}, Providers: []string{"openai", "anthropic"}}
		data, err := json.Marshal(original)
		require.NoError(t, err)

		var restored ModelSelector
		err = json.Unmarshal(data, &restored)
		require.NoError(t, err)
		assert.Equal(t, original, restored)
	})

	t.Run("string round trip", func(t *testing.T) {
		original := ModelSelector{ID: "gpt-4"}
		data, err := json.Marshal(original)
		require.NoError(t, err)
		assert.Equal(t, `"gpt-4"`, string(data))

		var restored ModelSelector
		err = json.Unmarshal(data, &restored)
		require.NoError(t, err)
		assert.Equal(t, original, restored)
	})
}

func TestModelSelector_UnmarshalYAML(t *testing.T) {
	t.Run("string format converts to ID", func(t *testing.T) {
		var m ModelSelector
		err := yaml.Unmarshal([]byte(`claude-4.5-sonnet`), &m)
		require.NoError(t, err)
		assert.Equal(t, "claude-4.5-sonnet", m.ID)
		assert.Empty(t, m.Tags)
		assert.Empty(t, m.Providers)
	})

	t.Run("string format trims whitespace", func(t *testing.T) {
		var m ModelSelector
		err := yaml.Unmarshal([]byte(`"  claude-4  "`), &m)
		require.NoError(t, err)
		assert.Equal(t, "claude-4", m.ID)
	})

	t.Run("struct format with id", func(t *testing.T) {
		var m ModelSelector
		err := yaml.Unmarshal([]byte("id: gpt-4"), &m)
		require.NoError(t, err)
		assert.Equal(t, "gpt-4", m.ID)
	})

	t.Run("struct format with tags", func(t *testing.T) {
		var m ModelSelector
		err := yaml.Unmarshal([]byte("tags:\n  - fast\n  - cheap"), &m)
		require.NoError(t, err)
		assert.Equal(t, []string{"fast", "cheap"}, m.Tags)
	})

	t.Run("struct format with id and tags", func(t *testing.T) {
		var m ModelSelector
		err := yaml.Unmarshal([]byte("id: gpt-4\ntags:\n  - fast"), &m)
		require.NoError(t, err)
		assert.Equal(t, "gpt-4", m.ID)
		assert.Equal(t, []string{"fast"}, m.Tags)
	})

	t.Run("struct format with providers", func(t *testing.T) {
		var m ModelSelector
		err := yaml.Unmarshal([]byte("tags:\n  - flagship\nproviders:\n  - anthropic\n  - openrouter"), &m)
		require.NoError(t, err)
		assert.Equal(t, []string{"flagship"}, m.Tags)
		assert.Equal(t, []string{"anthropic", "openrouter"}, m.Providers)
	})

	t.Run("pointer field in struct with string format", func(t *testing.T) {
		type TestStruct struct {
			Model *ModelSelector `yaml:"model"`
		}
		var ts TestStruct
		err := yaml.Unmarshal([]byte("model: claude-4"), &ts)
		require.NoError(t, err)
		require.NotNil(t, ts.Model)
		assert.Equal(t, "claude-4", ts.Model.ID)
	})

	t.Run("pointer field in struct with object format", func(t *testing.T) {
		type TestStruct struct {
			Model *ModelSelector `yaml:"model"`
		}
		var ts TestStruct
		err := yaml.Unmarshal([]byte("model:\n  id: claude-4"), &ts)
		require.NoError(t, err)
		require.NotNil(t, ts.Model)
		assert.Equal(t, "claude-4", ts.Model.ID)
	})
}
