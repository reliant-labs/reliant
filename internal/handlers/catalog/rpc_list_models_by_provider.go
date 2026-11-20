package catalog

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// ListModelsByProvider returns all models for a specific provider
func (s *Service) ListModelsByProvider(
	ctx context.Context,
	req *reliantv1.ListModelsByProviderRequest,
) (*reliantv1.ListModelsByProviderResponse, error) {
	provider := req.Provider

	// Get all models for the provider from the registry
	registry := models.MustGetRegistry()
	providerModels := registry.ListModelsByProvider(provider)
	if provider == "reliant" {
		filtered := make([]models.ModelDefinition, 0, len(providerModels))
		for _, def := range providerModels {
			if models.CanDriverUseModel("reliant", models.ModelID(def.ID)) {
				filtered = append(filtered, def)
			}
		}
		providerModels = filtered
	}

	// Convert to response format
	modelInfos := make([]*reliantv1.ModelInfo, 0, len(providerModels))
	for _, def := range providerModels {
		modelInfos = append(modelInfos, &reliantv1.ModelInfo{
			Id:                      def.ID,
			Name:                    def.Name,
			Provider:                provider,
			DriverId:                provider,
			ContextWindow:           int64(models.EffectiveContextWindow(&def, provider)),
			DefaultMaxTokens:        int64(models.EffectiveMaxOutputTokens(&def, provider)),
			CanReason:               def.Capabilities.CanReason,
			SupportedThinkingLevels: models.SupportedThinkingLevels(def.Capabilities),
		})
	}

	resp := &reliantv1.ListModelsByProviderResponse{
		Models: modelInfos,
	}
	return resp, nil
}
