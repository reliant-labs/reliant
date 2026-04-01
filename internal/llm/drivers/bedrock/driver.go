// Copyright (c) 2025 Reliant Labs
package bedrock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

const Family models.Family = "bedrock"

type bedrockOptions struct {
	// Bedrock specific options can be added here
}

type BedrockOption func(*bedrockOptions)

type BedrockClient struct {
	providerOptions llm.DriverOptions
	options         bedrockOptions
	childProvider   client
}

// Name returns the name of the driver
func (c *BedrockClient) Name() string {
	return "bedrock"
}

// client represents the internal client interface for drivers
type client interface {
	SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error)
	StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent
}

func NewClient(opts llm.DriverOptions) (*BedrockClient, error) {
	bedrockOpts := bedrockOptions{}
	// Apply bedrock specific options if they are added in the future

	// Get AWS region from environment
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}

	if region == "" {
		region = "us-east-1" // default region
	}
	if len(region) < 2 {
		return nil, errors.New("invalid AWS region")
	}

	// Prefix the model name with region
	regionPrefix := region[:2]
	modelName := opts.Model.APIModel
	opts.Model.APIModel = fmt.Sprintf("%s.%s", regionPrefix, modelName)

	// Determine which provider to use based on the model
	if strings.Contains(string(opts.Model.APIModel), "anthropic") {
		// Create Anthropic client with Bedrock configuration
		opts.UseBedrock = true
		opts.DisableCache = true
		return &BedrockClient{
			providerOptions: opts,
			options:         bedrockOpts,
			childProvider:   anthropic.NewAnthropicClient(opts),
		}, nil
	}

	// Return client with nil childProvider if model is not supported
	// This will cause an error when used
	return nil, fmt.Errorf("unsupported model for Bedrock provider: %s", opts.Model.APIModel)
}

func (b *BedrockClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	if b.childProvider == nil {
		return nil, errors.New("unsupported model for bedrock provider")
	}
	return b.childProvider.SendMessages(ctx, prompts, messages, tools)
}

func (b *BedrockClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	eventChan := make(chan llm.DriverEvent)

	if b.childProvider == nil {
		go func() {
			eventChan <- llm.DriverEvent{
				Type:  llm.EventError,
				Error: errors.New("unsupported model for bedrock provider"),
			}
			close(eventChan)
		}()
		return eventChan
	}

	return b.childProvider.StreamResponse(ctx, prompts, messages, tools)
}

func (b *BedrockClient) Model() models.Model {
	return b.providerOptions.Model
}

func (b *BedrockClient) ValidateKey(ctx context.Context) error {
	// Use Claude 3.5 Haiku for validation (small, fast model for Bedrock)
	testMessages := []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Say 'test' and nothing else"},
			},
		},
	}

	// Create a temporary client with the small model
	validationOpts := b.providerOptions
	registry := models.MustGetRegistry()
	if def, ok := registry.GetDefinition(string(models.Claude45Haiku)); ok {
		validationOpts.Model = def.ToModel()
	}
	validationOpts.MaxTokens = 10
	validationOpts.UseBedrock = true

	validationClient, err := NewClient(validationOpts)
	if err != nil {
		return err
	}

	_, err = validationClient.SendMessages(ctx, []string{}, testMessages, []tools.Tool{})
	return err
}
