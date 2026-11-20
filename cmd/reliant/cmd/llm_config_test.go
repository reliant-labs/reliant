// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/pkg/config"
)

var llmEnv = []string{
	"RELIANT_API_BASE_URL", "VERTEXAI_PROJECT", "VERTEXAI_LOCATION",
	"RELIANT_LLM_STREAM_IDLE_TIMEOUT", "RELIANT_LLM_STREAM_CONTENT_STALL_TIMEOUT",
	"RELIANT_LLM_STREAM_PROGRESS_TIMEOUT", "RELIANT_LLM_STREAM_MID_BLOCK_STALL_TIMEOUT",
	"RELIANT_DEBUG_OPENAI_RESPONSES_INPUT", "RELIANT_DEBUG_OPENAI_TOOL_SCHEMAS",
}

func isolateLLMEnv(t *testing.T) {
	t.Helper()
	isolateServerRoleEnv(t)
	for _, name := range llmEnv {
		t.Setenv(name, "") // restores the original afterwards
		require.NoError(t, os.Unsetenv(name))
	}
}

// llmOf loads the API server and the worker and returns the LLM settings
// each hands its package.
func llmOf(t *testing.T) (api, worker drivers.Settings, err error) {
	t.Helper()
	apiCmd, _, ferr := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, ferr)
	var a apiOptions
	if err := loadServerAPIConfig(apiCmd, &a); err != nil {
		return drivers.Settings{}, drivers.Settings{}, err
	}
	workerCmd, _, ferr := NewRootCmd().Find([]string{"server", "worker"})
	require.NoError(t, ferr)
	var w workerOptions
	err = loadServerWorkerConfig(workerCmd, &w)
	return a.LLM, w.LLM, err
}

// The defaults are the LLM layer's own; RELIANT_API_BASE_URL is what
// control-plane sets on both roles (dev host env, lib/env.k).
func TestLLMEnvContract(t *testing.T) {
	isolateLLMEnv(t)
	api, worker, err := llmOf(t)
	require.NoError(t, err)
	want := drivers.Settings{
		StreamTimeouts: llm.StreamTimeouts{Idle: llm.DefaultStreamIdleTimeout, ContentStall: llm.DefaultStreamContentStallTimeout,
			Progress: llm.DefaultStreamProgressTimeout},
		VertexLocation: "us-central1",
	}
	assert.Equal(t, want, api)
	assert.Equal(t, want, worker)

	for name, value := range map[string]string{
		"RELIANT_API_BASE_URL":                       "http://control-plane:8090/v1",
		"VERTEXAI_LOCATION":                          "europe-west4",
		"RELIANT_LLM_STREAM_IDLE_TIMEOUT":            "3m",
		"RELIANT_LLM_STREAM_CONTENT_STALL_TIMEOUT":   "12m",
		"RELIANT_LLM_STREAM_PROGRESS_TIMEOUT":        "20m",
		"RELIANT_LLM_STREAM_MID_BLOCK_STALL_TIMEOUT": "45m",
		"RELIANT_DEBUG_OPENAI_RESPONSES_INPUT":       "1",
		"RELIANT_DEBUG_OPENAI_TOOL_SCHEMAS":          "true",
	} {
		t.Setenv(name, value)
	}
	api, worker, err = llmOf(t)
	require.NoError(t, err)
	want = drivers.Settings{
		StreamTimeouts: llm.StreamTimeouts{Idle: 3 * time.Minute, ContentStall: 12 * time.Minute,
			Progress: 20 * time.Minute, MidBlock: 45 * time.Minute},
		ReliantBaseURL: "http://control-plane:8090/v1", VertexLocation: "europe-west4",
		DebugOpenAIResponsesInput: true, DebugOpenAIToolSchemas: true,
	}
	assert.Equal(t, want, api)
	assert.Equal(t, want, worker)
}

func TestLegacyVertexProjectConfigDoesNotChangeDriverSettings(t *testing.T) {
	isolateLLMEnv(t)
	wantAPI, wantWorker, err := llmOf(t)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "reliant.yaml")
	require.NoError(t, os.WriteFile(file, []byte("llm:\n  vertexai_project: legacy-file-project\n"), 0o600))
	assertLegacyProject := func(want string) {
		t.Helper()
		for _, role := range []string{"api", "worker"} {
			cmd, _, err := NewRootCmd().Find([]string{"server", role})
			require.NoError(t, err)
			process, err := config.LoadServerProcessConfig(cmd)
			require.NoError(t, err)
			// Read the deprecated binding without making the driver consume it.
			llmConfig := process.GetLlm().ProtoReflect()
			field := llmConfig.Descriptor().Fields().ByNumber(2)
			require.NotNil(t, field)
			assert.Equal(t, want, llmConfig.Get(field).String(), role)
		}
		api, worker, err := llmOf(t)
		require.NoError(t, err)
		assert.Equal(t, wantAPI, api)
		assert.Equal(t, wantWorker, worker)
	}
	t.Setenv("RELIANT_CONFIG", file)
	assertLegacyProject("legacy-file-project")
	t.Setenv("VERTEXAI_PROJECT", "legacy-env-project")
	assertLegacyProject("legacy-env-project")
}

// A deadline the guard cannot honour stops the server at load (each used to
// be ignored with a WARN, falling back to the default).
func TestLLMMisconfigurationFailsBoot(t *testing.T) {
	for name, value := range map[string]string{
		"RELIANT_LLM_STREAM_IDLE_TIMEOUT":            "nonsense",
		"RELIANT_LLM_STREAM_CONTENT_STALL_TIMEOUT":   "0s",
		"RELIANT_LLM_STREAM_PROGRESS_TIMEOUT":        "-1m",
		"RELIANT_LLM_STREAM_MID_BLOCK_STALL_TIMEOUT": "soon",
		"RELIANT_DEBUG_OPENAI_TOOL_SCHEMAS":          "yes",
	} {
		t.Run(name+"="+value, func(t *testing.T) {
			isolateLLMEnv(t)
			t.Setenv(name, value)
			_, _, err := llmOf(t)
			require.Error(t, err)
			assert.Contains(t, err.Error(), name)
		})
	}
}
