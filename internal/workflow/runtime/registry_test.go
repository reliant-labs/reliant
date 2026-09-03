// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"encoding/json"
	"testing"
)

// TestTemporalJSONSerialization verifies that Temporal's JSON serialization
// handles various input/output patterns correctly. This tests what Temporal does
// under the hood - no manual conversion code needed!
func TestTemporalJSONSerialization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    interface{}
		validate func(t *testing.T, jsonBytes []byte)
	}{
		{
			name: "struct with json tags serializes correctly",
			input: struct {
				ChatID string `json:"chat_id"`
				Thread string `json:"thread"`
			}{
				ChatID: "chat-123",
				Thread: "0",
			},
			validate: func(t *testing.T, jsonBytes []byte) {
				var result struct {
					ChatID string `json:"chat_id"`
					Thread string `json:"thread"`
				}
				if err := json.Unmarshal(jsonBytes, &result); err != nil {
					t.Fatalf("unmarshal error: %v", err)
				}
				if result.ChatID != "chat-123" {
					t.Errorf("expected chat_id='chat-123', got '%s'", result.ChatID)
				}
				if result.Thread != "0" {
					t.Errorf("expected thread='0', got '%s'", result.Thread)
				}
			},
		},
		{
			name:  "int serializes and deserializes correctly",
			input: 42,
			validate: func(t *testing.T, jsonBytes []byte) {
				var result int
				if err := json.Unmarshal(jsonBytes, &result); err != nil {
					t.Fatalf("unmarshal error: %v", err)
				}
				if result != 42 {
					t.Errorf("expected 42, got %d", result)
				}
			},
		},
		{
			name: "map serializes correctly",
			input: map[string]interface{}{
				"key1": "value1",
				"key2": float64(123), // JSON numbers are float64
			},
			validate: func(t *testing.T, jsonBytes []byte) {
				var result map[string]interface{}
				if err := json.Unmarshal(jsonBytes, &result); err != nil {
					t.Fatalf("unmarshal error: %v", err)
				}
				if result["key1"] != "value1" {
					t.Errorf("expected key1='value1', got '%v'", result["key1"])
				}
				// JSON numbers come back as float64
				if result["key2"] != float64(123) {
					t.Errorf("expected key2=123, got '%v'", result["key2"])
				}
			},
		},
		{
			name: "nested struct serializes correctly",
			input: struct {
				Message struct {
					Role string `json:"role"`
					Text string `json:"text"`
				} `json:"message"`
				TokenCount int `json:"token_count"`
			}{
				Message: struct {
					Role string `json:"role"`
					Text string `json:"text"`
				}{
					Role: "user",
					Text: "Hello",
				},
				TokenCount: 5,
			},
			validate: func(t *testing.T, jsonBytes []byte) {
				var result struct {
					Message struct {
						Role string `json:"role"`
						Text string `json:"text"`
					} `json:"message"`
					TokenCount int `json:"token_count"`
				}
				if err := json.Unmarshal(jsonBytes, &result); err != nil {
					t.Fatalf("unmarshal error: %v", err)
				}
				if result.Message.Role != "user" {
					t.Errorf("expected role='user', got '%s'", result.Message.Role)
				}
				if result.TokenCount != 5 {
					t.Errorf("expected token_count=5, got %d", result.TokenCount)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This is what Temporal does: JSON marshal/unmarshal
			jsonBytes, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}
			tt.validate(t, jsonBytes)
		})
	}
}

// TestActivityWrapperSupportsVariousTypes verifies that the wrapper signature
// allows activities to return various types (slices, structs, primitives)
func TestActivityWrapperSupportsVariousTypes(t *testing.T) {
	t.Parallel()
	// This test verifies that we can create wrappers for activities with different output types.
	// Temporal handles all serialization - we just need typed functions.

	t.Run("wrapper accepts activity returning []byte", func(t *testing.T) {
		activity := func(ctx context.Context, input string) ([]byte, error) {
			return []byte("test-output"), nil
		}

		// Should compile and create wrapper successfully
		wrapper := NewActivityWrapper("test-activity", activity, nil)
		if wrapper == nil {
			t.Fatal("expected wrapper to be created")
		}
	})

	t.Run("wrapper accepts activity returning struct", func(t *testing.T) {
		type Output struct {
			MessageID string
			Success   bool
		}

		activity := func(ctx context.Context, input string) (Output, error) {
			return Output{MessageID: "msg-123", Success: true}, nil
		}

		wrapper := NewActivityWrapper("test-activity", activity, nil)
		if wrapper == nil {
			t.Fatal("expected wrapper to be created")
		}
	})

	t.Run("wrapper accepts activity returning slice of structs", func(t *testing.T) {
		type Item struct {
			ID   string
			Name string
		}

		activity := func(ctx context.Context, input string) ([]Item, error) {
			return []Item{
				{ID: "1", Name: "item1"},
				{ID: "2", Name: "item2"},
			}, nil
		}

		wrapper := NewActivityWrapper("test-activity", activity, nil)
		if wrapper == nil {
			t.Fatal("expected wrapper to be created")
		}
	})

	t.Run("wrapper accepts activity returning primitive", func(t *testing.T) {
		activity := func(ctx context.Context, input int) (string, error) {
			return "result", nil
		}

		wrapper := NewActivityWrapper("test-activity", activity, nil)
		if wrapper == nil {
			t.Fatal("expected wrapper to be created")
		}
	})
}

// TestExtractChatID verifies that chat_id is correctly extracted from various input formats
func TestExtractChatID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: "",
		},
		{
			name: "map with chat_id",
			input: map[string]interface{}{
				"chat_id": "chat-123",
				"thread":  "0",
			},
			expected: "chat-123",
		},
		{
			name: "map without chat_id",
			input: map[string]interface{}{
				"thread":      "0",
				"other_field": "value",
			},
			expected: "",
		},
		{
			name: "struct with ChatID field",
			input: struct {
				ChatID string
				Thread string
			}{
				ChatID: "chat-456",
				Thread: "0",
			},
			expected: "chat-456",
		},
		{
			name: "struct with json tag chat_id",
			input: struct {
				ChatID string `json:"chat_id"`
				Thread string `json:"thread"`
			}{
				ChatID: "chat-789",
				Thread: "0",
			},
			expected: "chat-789",
		},
		{
			name: "pointer to struct",
			input: &struct {
				ChatID string `json:"chat_id"`
				Thread string `json:"thread"`
			}{
				ChatID: "chat-ptr",
				Thread: "0",
			},
			expected: "chat-ptr",
		},
		{
			name: "struct without chat_id field",
			input: struct {
				Thread     string
				OtherField string
			}{
				Thread:     "0",
				OtherField: "value",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractChatID(tt.input)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}
