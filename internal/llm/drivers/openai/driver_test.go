package openai

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
)

func TestCalculateCostMicros(t *testing.T) {
	client := &OpenaiClient{
		Options: llm.DriverOptions{
			Model: models.Model{
				CostPer1MIn:  2.0,
				CostPer1MOut: 8.0,
			},
		},
	}

	costMicros := client.calculateCostMicros(llm.TokenUsage{InputTokens: 2000, OutputTokens: 500})
	// 2000 input @ $2/1M = $0.004 = 4000 micros
	// 500 output @ $8/1M = $0.004 = 4000 micros
	// Total = 8000 micros
	assert.Equal(t, int64(8000), costMicros)
}
