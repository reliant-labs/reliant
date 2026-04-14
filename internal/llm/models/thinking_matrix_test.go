package models

import "testing"

func TestBuildThinkingCapabilityMatrix(t *testing.T) {
	defs := []ModelDefinition{
		{
			ID:         "gpt-5.5",
			Name:       "GPT-5.5",
			Visibility: VisibilityUser,
			Capabilities: ModelCapabilities{
				CanReason: true,
			},
			Providers: []ProviderMapping{
				{Driver: "codex", APIModel: "gpt-5.5"},
				{Driver: "openai", APIModel: "gpt-5.5"},
			},
		},
		{
			ID:         "gpt-5.4",
			Name:       "GPT-5.4",
			Visibility: VisibilityUser,
			Capabilities: ModelCapabilities{
				CanReason: true,
			},
			Providers: []ProviderMapping{
				{Driver: "codex", APIModel: "gpt-5.4"},
				{Driver: "openai", APIModel: "gpt-5.4"},
			},
		},
		{
			ID:         "gpt-5.4-pro",
			Name:       "GPT-5.4 Pro",
			Visibility: VisibilityUser,
			Capabilities: ModelCapabilities{
				CanReason: true,
			},
			Providers: []ProviderMapping{
				{Driver: "openai", APIModel: "gpt-5.4-pro"},
			},
		},
		{
			ID:         "gpt-5.3-codex",
			Name:       "GPT-5.3 Codex",
			Visibility: VisibilityUser,
			Capabilities: ModelCapabilities{
				CanReason: true,
			},
			Providers: []ProviderMapping{
				{Driver: "codex", APIModel: "gpt-5.3-codex"},
				{Driver: "openai", APIModel: "gpt-5.3-codex"},
			},
		},
		{
			ID:         "gpt-5.4-mini",
			Name:       "GPT-5.4 Mini",
			Visibility: VisibilityUser,
			Capabilities: ModelCapabilities{
				CanReason: true,
			},
			Providers: []ProviderMapping{
				{Driver: "codex", APIModel: "gpt-5.4-mini"},
				{Driver: "openai", APIModel: "gpt-5.4-mini"},
				{Driver: "openrouter", APIModel: "openai/gpt-5.4-mini"},
			},
		},
		{
			ID:         "gpt-5.3-codex-spark",
			Name:       "GPT-5.3 Codex Spark",
			Visibility: VisibilityUser,
			Capabilities: ModelCapabilities{
				CanReason: true,
			},
			Providers: []ProviderMapping{{Driver: "codex", APIModel: "gpt-5.3-codex-spark"}},
		},
		{
			ID:         "claude-4.5-haiku",
			Name:       "Claude 4.5 Haiku",
			Visibility: VisibilityUser,
			Capabilities: ModelCapabilities{
				CanReason: false,
			},
			Providers: []ProviderMapping{{Driver: "anthropic", APIModel: "claude-haiku-4-5"}},
		},
	}

	matrix := BuildThinkingCapabilityMatrix(defs)
	if len(matrix) != 12 {
		t.Fatalf("expected 12 matrix rows, got %d", len(matrix))
	}

	var gpt55CodexRow *ThinkingCapabilityMatrixEntry
	var gpt55OpenAIRow *ThinkingCapabilityMatrixEntry
	var gpt54CodexRow *ThinkingCapabilityMatrixEntry
	var gpt54OpenAIRow *ThinkingCapabilityMatrixEntry
	var gpt54ProOpenAIRow *ThinkingCapabilityMatrixEntry
	var miniCodexRow *ThinkingCapabilityMatrixEntry
	var miniOpenAIRow *ThinkingCapabilityMatrixEntry
	var miniOpenRouterRow *ThinkingCapabilityMatrixEntry
	var sparkRow *ThinkingCapabilityMatrixEntry
	var openAIRow *ThinkingCapabilityMatrixEntry
	var noReasonRow *ThinkingCapabilityMatrixEntry
	for i := range matrix {
		row := &matrix[i]
		switch row.ModelID + "@" + row.DriverID {
		case "gpt-5.5@codex":
			gpt55CodexRow = row
		case "gpt-5.5@openai":
			gpt55OpenAIRow = row
		case "gpt-5.4@codex":
			gpt54CodexRow = row
		case "gpt-5.4@openai":
			gpt54OpenAIRow = row
		case "gpt-5.4-pro@openai":
			gpt54ProOpenAIRow = row
		case "gpt-5.4-mini@codex":
			miniCodexRow = row
		case "gpt-5.4-mini@openai":
			miniOpenAIRow = row
		case "gpt-5.4-mini@openrouter":
			miniOpenRouterRow = row
		case "gpt-5.3-codex-spark@codex":
			sparkRow = row
		case "gpt-5.3-codex@openai":
			openAIRow = row
		case "claude-4.5-haiku@anthropic":
			noReasonRow = row
		}
	}

	if gpt55CodexRow == nil {
		t.Fatal("missing gpt-5.5 codex row")
	}
	if len(gpt55CodexRow.Levels) != 4 || gpt55CodexRow.Levels[3] != "xhigh" {
		t.Fatalf("expected gpt-5.5 codex row to include xhigh, got %v", gpt55CodexRow.Levels)
	}
	if gpt55OpenAIRow == nil {
		t.Fatal("missing gpt-5.5 openai row")
	}
	if len(gpt55OpenAIRow.Levels) != 4 || gpt55OpenAIRow.Levels[3] != "xhigh" {
		t.Fatalf("expected gpt-5.5 openai row to include xhigh, got %v", gpt55OpenAIRow.Levels)
	}

	if gpt54CodexRow == nil {
		t.Fatal("missing gpt-5.4 codex row")
	}
	if len(gpt54CodexRow.Levels) != 4 || gpt54CodexRow.Levels[3] != "xhigh" {
		t.Fatalf("expected gpt-5.4 codex row to include xhigh, got %v", gpt54CodexRow.Levels)
	}
	if gpt54CodexRow.DefaultLevel != "medium" {
		t.Fatalf("expected gpt-5.4 codex default medium, got %q", gpt54CodexRow.DefaultLevel)
	}

	if gpt54OpenAIRow == nil {
		t.Fatal("missing gpt-5.4 openai row")
	}
	if len(gpt54OpenAIRow.Levels) != 4 || gpt54OpenAIRow.Levels[3] != "xhigh" {
		t.Fatalf("expected gpt-5.4 openai row to include xhigh, got %v", gpt54OpenAIRow.Levels)
	}

	if gpt54ProOpenAIRow == nil {
		t.Fatal("missing gpt-5.4-pro openai row")
	}
	if len(gpt54ProOpenAIRow.Levels) != 3 || gpt54ProOpenAIRow.Levels[0] != "medium" || gpt54ProOpenAIRow.Levels[2] != "xhigh" {
		t.Fatalf("expected gpt-5.4-pro openai row to be [medium high xhigh], got %v", gpt54ProOpenAIRow.Levels)
	}

	if miniCodexRow == nil {
		t.Fatal("missing gpt-5.4-mini codex row")
	}
	if len(miniCodexRow.Levels) != 4 || miniCodexRow.Levels[3] != "xhigh" {
		t.Fatalf("expected gpt-5.4-mini codex row to include xhigh, got %v", miniCodexRow.Levels)
	}
	if miniOpenAIRow == nil {
		t.Fatal("missing gpt-5.4-mini openai row")
	}
	if len(miniOpenAIRow.Levels) != 4 || miniOpenAIRow.Levels[3] != "xhigh" {
		t.Fatalf("expected gpt-5.4-mini openai row to include xhigh, got %v", miniOpenAIRow.Levels)
	}
	if miniOpenRouterRow == nil {
		t.Fatal("missing gpt-5.4-mini openrouter row")
	}
	if len(miniOpenRouterRow.Levels) != 4 || miniOpenRouterRow.Levels[3] != "xhigh" {
		t.Fatalf("expected gpt-5.4-mini openrouter row to include xhigh, got %v", miniOpenRouterRow.Levels)
	}

	if sparkRow == nil {
		t.Fatal("missing spark row")
	}
	if len(sparkRow.Levels) != 4 || sparkRow.Levels[3] != "xhigh" {
		t.Fatalf("expected spark row to include xhigh, got %v", sparkRow.Levels)
	}

	if openAIRow == nil {
		t.Fatal("missing openai row")
	}
	if len(openAIRow.Levels) != 3 {
		t.Fatalf("expected openai row to have 3 levels, got %v", openAIRow.Levels)
	}

	if noReasonRow == nil {
		t.Fatal("missing non-reasoning row")
	}
	if noReasonRow.SupportsThinking {
		t.Fatal("expected non-reasoning row to not support thinking")
	}
	if len(noReasonRow.Levels) != 0 {
		t.Fatalf("expected non-reasoning row to have empty levels, got %v", noReasonRow.Levels)
	}
}
