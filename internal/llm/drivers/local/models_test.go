// Copyright (c) 2025 Reliant Labs
package local

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

func TestConvertLocalModelToDefinition(t *testing.T) {
	tests := []struct {
		name     string
		input    localModel
		wantID   string
		wantName string
		wantCtx  int
	}{
		{
			name: "basic ollama model",
			input: localModel{
				ID:     "qwen3:latest",
				Object: "model",
			},
			wantID:   "local-qwen3-latest",
			wantName: "Qwen3 latest",
			wantCtx:  DefaultContextWindow, // Uses default when not provided
		},
		{
			name: "model with context length",
			input: localModel{
				ID:               "llama3.3:70b",
				Object:           "model",
				MaxContextLength: 131072,
			},
			wantID:   "local-llama3.3-70b",
			wantName: "Llama3",
			wantCtx:  131072,
		},
		{
			name: "model with loaded context (takes priority)",
			input: localModel{
				ID:                  "deepseek-r1:32b",
				Object:              "model",
				MaxContextLength:    131072,
				LoadedContextLength: 65536, // Actually loaded with smaller context
			},
			wantID:   "local-deepseek-r1-32b",
			wantName: "Deepseek R1 32b",
			wantCtx:  65536,
		},
		{
			name: "lm studio style model with full metadata",
			input: localModel{
				ID:                  "TheBloke/Mistral-7B-v0.1-GGUF",
				Object:              "model",
				Type:                "llm",
				Publisher:           "TheBloke",
				Arch:                "mistral",
				Quantization:        "Q4_K_M",
				MaxContextLength:    32768,
				LoadedContextLength: 32768,
			},
			wantID:   "local-thebloke-mistral-7b-v0.1-gguf",
			wantName: "Mistral 7B V0",
			wantCtx:  32768,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertLocalModelToDefinition(tt.input)

			if got.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", got.ID, tt.wantID)
			}

			if got.Capabilities.MaxContextWindow != tt.wantCtx {
				t.Errorf("MaxContextWindow = %d, want %d", got.Capabilities.MaxContextWindow, tt.wantCtx)
			}

			// Verify default capabilities
			if !got.Capabilities.SupportsTools {
				t.Error("SupportsTools should be true")
			}
			if !got.Capabilities.SupportsStreaming {
				t.Error("SupportsStreaming should be true")
			}
			if !got.Capabilities.SupportsAttachments {
				t.Error("SupportsAttachments should be true")
			}

			// Verify tags
			if len(got.Tags) != 1 || got.Tags[0] != "local" {
				t.Errorf("Tags = %v, want [local]", got.Tags)
			}

			// Verify provider mapping
			if len(got.Providers) != 1 {
				t.Fatalf("Providers count = %d, want 1", len(got.Providers))
			}
			if got.Providers[0].Driver != "local" {
				t.Errorf("Provider.Driver = %q, want 'local'", got.Providers[0].Driver)
			}
			if got.Providers[0].APIModel != tt.input.ID {
				t.Errorf("Provider.APIModel = %q, want %q", got.Providers[0].APIModel, tt.input.ID)
			}
		})
	}
}

func TestSanitizeModelID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"qwen3:latest", "qwen3-latest"},
		{"llama3.3:70b", "llama3.3-70b"},
		{"TheBloke/Mistral-7B", "thebloke-mistral-7b"},
		{"model@v1.0", "model-v1.0"},
		{"some  model", "some-model"},
		{"--trimmed--", "trimmed"},
		{"a/b:c@d", "a-b-c-d"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeModelID(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeModelID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFriendlyModelName(t *testing.T) {
	// friendlyModelName extracts a human-readable name from model IDs
	// The tag after : is stripped, and only the model family/version is extracted
	tests := []struct {
		input string
		want  string
	}{
		{"qwen3:latest", "Qwen3"},
		{"llama3.3:70b", "Llama3"},
		{"deepseek-r1:32b", "Deepseek R1"},
		{"mistral-7b-instruct-v0.2", "Mistral 7 B"}, // regex extracts limited info
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := friendlyModelName(tt.input)
			if got != tt.want {
				t.Errorf("friendlyModelName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDiscoverModels(t *testing.T) {
	tests := []struct {
		name           string
		response       localModelList
		statusCode     int
		wantCount      int
		wantFirstID    string
		wantFirstModel string
	}{
		{
			name: "ollama minimal response",
			response: localModelList{
				Data: []localModel{
					{ID: "qwen3:latest", Object: "model"},
					{ID: "llama3.3:70b", Object: "model"},
				},
			},
			statusCode:     http.StatusOK,
			wantCount:      2,
			wantFirstID:    "local-qwen3-latest",
			wantFirstModel: "qwen3:latest",
		},
		{
			name: "ollama with context info",
			response: localModelList{
				Data: []localModel{
					{
						ID:               "deepseek-r1:70b",
						Object:           "model",
						MaxContextLength: 131072,
					},
				},
			},
			statusCode:     http.StatusOK,
			wantCount:      1,
			wantFirstID:    "local-deepseek-r1-70b",
			wantFirstModel: "deepseek-r1:70b",
		},
		{
			name:       "empty response",
			response:   localModelList{Data: []localModel{}},
			statusCode: http.StatusOK,
			wantCount:  0,
		},
		{
			name:       "server error",
			response:   localModelList{},
			statusCode: http.StatusInternalServerError,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					json.NewEncoder(w).Encode(tt.response)
				}
			}))
			defer server.Close()

			got, err := DiscoverModels(server.URL)
			if err != nil {
				t.Fatalf("DiscoverModels() error = %v", err)
			}

			if len(got) != tt.wantCount {
				t.Errorf("DiscoverModels() returned %d models, want %d", len(got), tt.wantCount)
			}

			if tt.wantCount > 0 {
				if got[0].ID != tt.wantFirstID {
					t.Errorf("First model ID = %q, want %q", got[0].ID, tt.wantFirstID)
				}
				if got[0].Providers[0].APIModel != tt.wantFirstModel {
					t.Errorf("First model APIModel = %q, want %q", got[0].Providers[0].APIModel, tt.wantFirstModel)
				}
			}
		})
	}
}

func TestDiscoverModelsLMStudioFiltering(t *testing.T) {
	// LM Studio returns embeddings and other non-LLM models that should be filtered
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate LM Studio beta API path
		if r.URL.Path == "/api/v0/models" {
			json.NewEncoder(w).Encode(localModelList{
				Data: []localModel{
					{ID: "mistral-7b", Object: "model", Type: "llm"},
					{ID: "bge-large", Object: "model", Type: "embedding"}, // Should be filtered
					{ID: "whisper", Object: "model", Type: "asr"},         // Should be filtered
				},
			})
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	got, err := DiscoverModels(server.URL)
	if err != nil {
		t.Fatalf("DiscoverModels() error = %v", err)
	}

	if len(got) != 1 {
		t.Errorf("DiscoverModels() returned %d models, want 1 (only LLM type)", len(got))
	}

	if len(got) > 0 && got[0].Providers[0].APIModel != "mistral-7b" {
		t.Errorf("Expected mistral-7b, got %q", got[0].Providers[0].APIModel)
	}
}

func TestDiscoverModelsFallback(t *testing.T) {
	// Test that we fall back from LM Studio beta API to standard v1/models
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch r.URL.Path {
		case "/api/v0/models":
			// LM Studio beta API returns empty
			json.NewEncoder(w).Encode(localModelList{Data: []localModel{}})
		case "/v1/models":
			// Standard endpoint has models
			json.NewEncoder(w).Encode(localModelList{
				Data: []localModel{
					{ID: "qwen3:latest", Object: "model"},
				},
			})
		}
	}))
	defer server.Close()

	got, err := DiscoverModels(server.URL)
	if err != nil {
		t.Fatalf("DiscoverModels() error = %v", err)
	}

	if len(got) != 1 {
		t.Errorf("DiscoverModels() returned %d models, want 1", len(got))
	}

	if callCount != 2 {
		t.Errorf("Expected 2 API calls (beta then standard), got %d", callCount)
	}
}

func TestDefaultContextWindow(t *testing.T) {
	// Verify the default is appropriate for Reliant's needs
	// System prompts + tools are ~20k, so we need at least that plus room for conversation
	if DefaultContextWindow < 50000 {
		t.Errorf("DefaultContextWindow = %d, should be at least 50000 for Reliant's system prompts", DefaultContextWindow)
	}

	// Verify it's used when model doesn't report context
	model := localModel{ID: "test", Object: "model"}
	def := convertLocalModelToDefinition(model)

	if def.Capabilities.MaxContextWindow != DefaultContextWindow {
		t.Errorf("MaxContextWindow = %d, want %d (DefaultContextWindow)", def.Capabilities.MaxContextWindow, DefaultContextWindow)
	}
}

func TestConvertToDefinitions(t *testing.T) {
	input := []localModel{
		{ID: "model1:latest", Object: "model"},
		{ID: "model2:7b", Object: "model", MaxContextLength: 32768},
	}

	got := convertToDefinitions(input)

	if len(got) != 2 {
		t.Fatalf("convertToDefinitions() returned %d, want 2", len(got))
	}

	// Verify each model is converted correctly
	if got[0].ID != "local-model1-latest" {
		t.Errorf("First model ID = %q, want 'local-model1-latest'", got[0].ID)
	}
	if got[1].ID != "local-model2-7b" {
		t.Errorf("Second model ID = %q, want 'local-model2-7b'", got[1].ID)
	}

	// Verify context windows
	if got[0].Capabilities.MaxContextWindow != DefaultContextWindow {
		t.Errorf("First model context = %d, want %d", got[0].Capabilities.MaxContextWindow, DefaultContextWindow)
	}
	if got[1].Capabilities.MaxContextWindow != 32768 {
		t.Errorf("Second model context = %d, want 32768", got[1].Capabilities.MaxContextWindow)
	}
}

// TestRegistryIntegration tests that discovered models integrate correctly with the registry
func TestRegistryIntegration(t *testing.T) {
	// Create a mock discoverer
	mockModels := []localModel{
		{ID: "qwen3:latest", Object: "model"},
		{ID: "llama3.3:70b", Object: "model", MaxContextLength: 131072},
	}

	discoverer := func(baseURL string) ([]models.ModelDefinition, error) {
		return convertToDefinitions(mockModels), nil
	}

	// Create user config with local provider
	cfg := &models.UserModelsConfig{
		Providers: models.UserProvidersConfig{
			Local: &models.LocalProviderConfig{
				BaseURL: "http://localhost:11434/v1",
			},
		},
	}

	// Create registry with discovery
	reg, err := models.CreateRegistryWithDiscovery(cfg, discoverer)
	if err != nil {
		t.Fatalf("CreateRegistryWithDiscovery() error = %v", err)
	}

	// Verify models were added
	qwen, ok := reg.GetDefinition("local-qwen3-latest")
	if !ok || qwen == nil {
		t.Error("Expected to find local-qwen3-latest in registry")
	}

	llama, ok := reg.GetDefinition("local-llama3.3-70b")
	if !ok || llama == nil {
		t.Error("Expected to find local-llama3.3-70b in registry")
	}

	// Verify they have the "local" tag
	localModels := reg.GetModelsByTag("local")
	if len(localModels) < 2 {
		t.Errorf("Expected at least 2 local models, got %d", len(localModels))
	}
}
