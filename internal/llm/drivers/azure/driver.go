// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// This package implements the llm.Driver interface declared in
// internal/llm/types.go. Its behavioral contract already exists upstream, and
// the exported methods here are that interface's implementation plus
// provider-specific wire handling. A local contract.go would restate an
// interface this package does not own.
package azure

import (
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/openai/openai-go/v3/azure"
	"github.com/openai/openai-go/v3/option"
	"github.com/reliant-labs/reliant/internal/llm"
	openaiPkg "github.com/reliant-labs/reliant/internal/llm/drivers/openai"
)

type AzureClient struct {
	*openaiPkg.OpenaiClient
}

// Name returns the name of the driver
func (c *AzureClient) Name() string {
	return "azure"
}

func NewClient(opts llm.DriverOptions) *AzureClient {

	endpoint := os.Getenv("AZURE_OPENAI_ENDPOINT")      // ex: https://foo.openai.azure.com
	apiVersion := os.Getenv("AZURE_OPENAI_API_VERSION") // ex: 2025-04-01-preview

	if endpoint == "" || apiVersion == "" {
		return &AzureClient{OpenaiClient: openaiPkg.NewClient(opts)}
	}

	reqOpts := []option.RequestOption{
		azure.WithEndpoint(endpoint, apiVersion),
	}

	if opts.ApiKey != "" || os.Getenv("AZURE_OPENAI_API_KEY") != "" {
		key := opts.ApiKey
		if key == "" {
			key = os.Getenv("AZURE_OPENAI_API_KEY")
		}
		reqOpts = append(reqOpts, azure.WithAPIKey(key))
	} else if cred, err := azidentity.NewDefaultAzureCredential(nil); err == nil {
		reqOpts = append(reqOpts, azure.WithTokenCredential(cred))
	}

	base := &openaiPkg.OpenaiClient{
		Options: opts,
		Client:  llm.NewOpenAISDKClient(reqOpts...),
	}

	return &AzureClient{OpenaiClient: base}
}
