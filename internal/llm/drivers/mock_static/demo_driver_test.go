// Copyright (c) 2025 Reliant Labs
package mockstatic

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDemoDriver_SendMessages(t *testing.T) {
	exchanges := []DemoExchange{
		{
			SendMessage:      "hello",
			ExpectedContains: []string{"Hi there", "How can I help"},
		},
		{
			SendMessage:      "what is 2+2",
			ExpectedContains: []string{"4", "four"},
		},
		{
			SendMessage:      "goodbye",
			ExpectedContains: []string{"Bye", "See you"},
		},
	}

	driver := NewDemoDriver(exchanges)
	ctx := context.Background()

	// First exchange
	messages := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello world"}}},
	}

	resp, err := driver.SendMessages(ctx, nil, messages, nil)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "Hi there")
	assert.Contains(t, resp.Content, "How can I help")

	// Second exchange
	messages = append(messages, message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: resp.Content}}})
	messages = append(messages, message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "what is 2+2?"}}})

	resp, err = driver.SendMessages(ctx, nil, messages, nil)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "4")
	assert.Contains(t, resp.Content, "four")

	// Third exchange
	messages = append(messages, message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: resp.Content}}})
	messages = append(messages, message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "goodbye!"}}})

	resp, err = driver.SendMessages(ctx, nil, messages, nil)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "Bye")
	assert.Contains(t, resp.Content, "See you")

	// Fourth exchange should fail (no more exchanges)
	messages = append(messages, message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: resp.Content}}})
	messages = append(messages, message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "one more thing"}}})

	_, err = driver.SendMessages(ctx, nil, messages, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no more exchanges available")
}

func TestDemoDriver_ValidateExchange(t *testing.T) {
	exchanges := []DemoExchange{
		{
			SendMessage:      "test",
			ExpectedContains: []string{"response", "test"},
		},
	}

	driver := NewDemoDriver(exchanges)
	ctx := context.Background()

	messages := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "test message"}}},
	}

	resp, err := driver.SendMessages(ctx, nil, messages, nil)
	require.NoError(t, err)

	messages = append(messages, message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: resp.Content}}})

	// Validation should pass
	err = driver.ValidateExchange(messages)
	assert.NoError(t, err)

	// Test failed validation
	messages[1].Parts = []message.ContentPart{message.TextContent{Text: "wrong content"}}
	err = driver.ValidateExchange(messages)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "response missing expected content")
}

func TestDemoDriver_StrictValidation(t *testing.T) {
	exchanges := []DemoExchange{
		{
			SendMessage:      "exact match",
			ExpectedContains: []string{"response"},
		},
	}

	driver := NewDemoDriver(exchanges)
	ctx := context.Background()

	// With strict validation, should fail if message doesn't contain expected
	messages := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "different message"}}},
	}

	_, err := driver.SendMessages(ctx, nil, messages, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected message to contain")

	// Disable strict validation
	driver.SetStrictValidation(false)
	driver.Reset()

	_, err = driver.SendMessages(ctx, nil, messages, nil)
	assert.NoError(t, err)
}

func TestDemoDriver_StreamResponse(t *testing.T) {
	exchanges := []DemoExchange{
		{
			SendMessage:      "stream",
			ExpectedContains: []string{"streaming", "response"},
		},
	}

	driver := NewDemoDriver(exchanges)
	ctx := context.Background()

	messages := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "stream test"}}},
	}

	ch := driver.StreamResponse(ctx, nil, messages, nil)

	var result strings.Builder
	for event := range ch {
		if event.Error != nil {
			t.Fatalf("unexpected error: %v", event.Error)
		}
		result.WriteString(event.Content)
	}

	response := result.String()
	assert.Contains(t, response, "streaming")
	assert.Contains(t, response, "response")
}

func TestDemoDriver_Reset(t *testing.T) {
	exchanges := []DemoExchange{
		{
			SendMessage:      "first",
			ExpectedContains: []string{"first response"},
		},
		{
			SendMessage:      "second",
			ExpectedContains: []string{"second response"},
		},
	}

	driver := NewDemoDriver(exchanges)
	ctx := context.Background()

	// Use first exchange
	messages := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first message"}}},
	}

	resp, err := driver.SendMessages(ctx, nil, messages, nil)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "first response")

	// Reset and use first exchange again
	driver.Reset()

	resp, err = driver.SendMessages(ctx, nil, messages, nil)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "first response")
}
