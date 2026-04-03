package reliant

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/llm"
	openaiDriver "github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const Family models.Family = "reliant"

func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	if strings.TrimSpace(opts.ApiKey) == "" {
		return nil, fmt.Errorf("reliant runtime access key is required")
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, fmt.Errorf("reliant runtime base URL is required")
	}
	// Reliant runtime is an OpenAI-compatible gateway that expects canonical Reliant model IDs.
	opts.Model.APIModel = string(opts.Model.ID)
	return openaiDriver.NewClient(*opts), nil
}
