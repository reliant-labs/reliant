// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"google.golang.org/genai"
)

// providerType identifies which provider backend to use
type providerType string

const (
	providerGemini providerType = "gemini"
	providerClaude providerType = "claude"
)

// VertexAIClient is a multi-provider client for Vertex AI Model Garden
// It supports Gemini models via the genai SDK and Claude models via Vertex AI API
type VertexAIClient struct {
	options      llm.DriverOptions
	provider     providerType
	geminiClient *genai.Client
	// TODO: Add Claude client when implementing Claude support
	// claudeClient *vertexaiClaude.Client
}

// Name returns the name of the driver
func (c *VertexAIClient) Name() string {
	return "vertexai"
}

// NewClient creates a new Vertex AI client that can handle multiple providers
func NewClient(opts llm.DriverOptions) (*VertexAIClient, error) {
	// Determine which provider to use based on the model
	provider := getProviderForModel(opts.Model.ID)

	client := &VertexAIClient{
		options:  opts,
		provider: provider,
	}

	// Initialize the appropriate provider client
	switch provider {
	case providerGemini:
		if err := client.initGeminiClient(); err != nil {
			return nil, fmt.Errorf("failed to initialize Gemini client: %w", err)
		}
	case providerClaude:
		if err := client.initClaudeClient(); err != nil {
			return nil, fmt.Errorf("failed to initialize Claude client: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported provider for model %s", opts.Model.ID)
	}

	return client, nil
}

// getProviderForModel determines which provider to use based on the model ID
func getProviderForModel(modelID models.ModelID) providerType {
	modelStr := string(modelID)
	if strings.Contains(modelStr, "claude") {
		return providerClaude
	}
	// Default to Gemini for gemini models
	return providerGemini
}

// initGeminiClient initializes the Gemini provider client
func (c *VertexAIClient) initGeminiClient() error {
	project := os.Getenv("VERTEXAI_PROJECT")
	location := os.Getenv("VERTEXAI_LOCATION")

	if project == "" {
		return fmt.Errorf("VERTEXAI_PROJECT environment variable is required")
	}
	if location == "" {
		location = "us-central1" // Default location
		logging.Info("VERTEXAI_LOCATION not set, using default", "location", location)
	}

	client, err := llm.NewGenAISDKClient(context.Background(), &genai.ClientConfig{
		Project:  project,
		Location: location,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return err
	}

	c.geminiClient = client
	return nil
}

// initClaudeClient initializes the Claude provider client via Vertex AI
func (c *VertexAIClient) initClaudeClient() error {
	// Claude uses REST API with Google Cloud OAuth2, no SDK client needed
	// We just need to validate the environment variables
	project := os.Getenv("VERTEXAI_PROJECT")
	location := os.Getenv("VERTEXAI_LOCATION")

	if project == "" {
		return fmt.Errorf("VERTEXAI_PROJECT environment variable is required")
	}
	if location == "" {
		logging.Info("VERTEXAI_LOCATION not set, using default", "location", "us-central1")
	}

	return nil
}

// SendMessages sends messages to the appropriate provider
func (c *VertexAIClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	switch c.provider {
	case providerGemini:
		return c.sendMessagesGemini(ctx, prompts, messages, tools)
	case providerClaude:
		return c.sendMessagesClaude(ctx, prompts, messages, tools)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", c.provider)
	}
}

// StreamResponse streams responses from the appropriate provider
func (c *VertexAIClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	switch c.provider {
	case providerGemini:
		return c.streamResponseGemini(ctx, prompts, messages, tools)
	case providerClaude:
		return c.streamResponseClaude(ctx, prompts, messages, tools)
	default:
		ch := make(chan llm.DriverEvent, 1)
		ch <- llm.DriverEvent{
			Type:  llm.EventError,
			Error: fmt.Errorf("unsupported provider: %s", c.provider),
		}
		close(ch)
		return ch
	}
}

// Model returns the model configuration
func (c *VertexAIClient) Model() models.Model {
	return c.options.Model
}

// ValidateKey validates the Vertex AI configuration
func (c *VertexAIClient) ValidateKey(ctx context.Context) error {
	// For Vertex AI, we validate by attempting to create a client
	// and making a minimal request
	switch c.provider {
	case providerGemini:
		return c.validateGeminiKey(ctx)
	case providerClaude:
		return c.validateClaudeKey(ctx)
	default:
		return fmt.Errorf("unsupported provider: %s", c.provider)
	}
}
