// Copyright (c) 2025 Reliant Labs
package mockstatic

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/tokens"
)

type DemoMessage struct {
	Content string
	IsUser  bool
}

type DemoExchange struct {
	SendMessage      string
	ExpectedContains []string
}

type DemoDriver struct {
	model            models.Model
	exchanges        []DemoExchange
	currentExchange  int
	strictValidation bool
}

// Name returns the name of the driver
func (d *DemoDriver) Name() string {
	return "demo"
}

func NewDemoDriver(exchanges []DemoExchange) *DemoDriver {
	return &DemoDriver{
		model: models.Model{
			Name: "demo",
		},
		exchanges:        exchanges,
		currentExchange:  0,
		strictValidation: true,
	}
}

func (d *DemoDriver) Model() models.Model {
	return d.model
}

func (d *DemoDriver) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) (*llm.DriverResponse, error) {
	if d.currentExchange >= len(d.exchanges) {
		return nil, fmt.Errorf("demo driver: no more exchanges available (at exchange %d of %d)", d.currentExchange+1, len(d.exchanges))
	}

	exchange := d.exchanges[d.currentExchange]

	// Get the last user message
	var lastUserMessage string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == message.User {
			lastUserMessage = messages[i].Content().Text
			break
		}
	}

	// Validate the incoming message if we're in strict mode
	if d.strictValidation && exchange.SendMessage != "" {
		if !strings.Contains(lastUserMessage, exchange.SendMessage) {
			return nil, fmt.Errorf("demo driver: expected message to contain '%s', got '%s'", exchange.SendMessage, lastUserMessage)
		}
	}

	// Build response based on expected contains
	responseContent := strings.Join(exchange.ExpectedContains, " ")
	if responseContent == "" {
		responseContent = fmt.Sprintf("Demo response for exchange %d", d.currentExchange+1)
	}

	d.currentExchange++

	return &llm.DriverResponse{
		Content: responseContent,
		Usage: llm.TokenUsage{
			TokenCount: int64(tokens.EstimateTokens(lastUserMessage)),
		},
	}, nil
}

func (d *DemoDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent, 1)

	go func() {
		defer close(ch)

		resp, err := d.SendMessages(ctx, prompts, messages, tools)
		if err != nil {
			ch <- llm.DriverEvent{Error: err}
			return
		}

		// Stream the response character by character for demo purposes
		for _, char := range resp.Content {
			select {
			case <-ctx.Done():
				ch <- llm.DriverEvent{Error: ctx.Err()}
				return
			case ch <- llm.DriverEvent{Content: string(char)}:
			}
		}
	}()

	return ch
}

func (d *DemoDriver) Reset() {
	d.currentExchange = 0
}

func (d *DemoDriver) SetStrictValidation(strict bool) {
	d.strictValidation = strict
}

func (d *DemoDriver) ValidateKey(ctx context.Context) error {
	// Mock driver always validates successfully
	return nil
}

func (d *DemoDriver) ValidateExchange(messages []message.Message) error {
	if d.currentExchange == 0 || d.currentExchange > len(d.exchanges) {
		return fmt.Errorf("invalid exchange index: %d", d.currentExchange)
	}

	exchange := d.exchanges[d.currentExchange-1]

	// Find the last assistant message
	var lastAssistantMessage string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == message.Assistant {
			lastAssistantMessage = messages[i].Content().Text
			break
		}
	}

	// Validate that the assistant's response contains all expected strings
	for _, expected := range exchange.ExpectedContains {
		if !strings.Contains(lastAssistantMessage, expected) {
			return fmt.Errorf("response missing expected content '%s', got: %s", expected, lastAssistantMessage)
		}
	}

	return nil
}
