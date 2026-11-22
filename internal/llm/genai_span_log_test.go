// Copyright (c) 2025 Reliant Labs
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

func TestGenAISpanErrorLogOmitsProviderText(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	_, span := StartGenAISpan(context.Background(), "openai", models.Model{APIModel: "test-model"}, true)
	defer span.End()
	const secret = "private-prompt-and-api-key"
	span.RecordError(errors.New("provider echoed " + secret))

	if bytes.Contains(output.Bytes(), []byte(secret)) {
		t.Fatal("provider error text leaked into the log")
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode warning log: %v", err)
	}
	for key, want := range map[string]string{
		"level": "WARN", "msg": "LLM provider call failed",
		telemetry.AttrErrorType: ErrorType(errors.New("provider echoed " + secret)),
		"provider":              "openai", "model": "test-model",
	} {
		if record[key] != want {
			t.Errorf("log %s = %v, want %s", key, record[key], want)
		}
	}
	if _, ok := record["error"]; ok {
		t.Fatal("warning must not include a raw error field")
	}
}
