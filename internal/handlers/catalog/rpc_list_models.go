package catalog

import (
	"context"
	"sort"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/modelprefs"
)

// ListModels returns all available models filtered by user's configured API keys
// Each model is returned once per available driver, allowing users to choose which API to use
func (s *Service) ListModels(
	ctx context.Context,
	req *connect.Request[reliantv1.ListModelsRequest],
) (*connect.Response[reliantv1.ListModelsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	scope, err := s.pickerScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	// The drivers the scope may run on: the person's own accounts, and in an
	// org its accounts and seats, narrowed by its policy.
	availableDrivers := drivers.GetAvailableDrivers(ctx, scope)

	// Per-account availability (Enabled per {driver, model}) comes from the
	// modular provider capability, not a provider special-case. Static providers
	// report every model enabled; a dynamic provider (Copilot) reports its
	// account's policy. Fails open per provider, so a lookup failure never blanks
	// the picker. We index it by driver -> model id -> enabled to gate the loop
	// below uniformly.
	enabledByDriver := make(map[string]map[string]bool)
	for _, mi := range drivers.ListAvailableModelsOrEmpty(ctx, scope) {
		byModel := enabledByDriver[mi.DriverID]
		if byModel == nil {
			byModel = make(map[string]bool)
			enabledByDriver[mi.DriverID] = byModel
		}
		byModel[mi.ID] = mi.Enabled
	}

	// Get all user-visible models from the registry
	registry := models.MustGetRegistry().WithAvailability(availableDrivers.Availability)
	allModels := registry.GetUserVisibleModels()

	logging.DebugContext(ctx, "[ListModels] Building model list for user", "userID", userID, "totalModels", len(allModels), "availableDrivers", len(availableDrivers.Drivers))

	// Build list of all model+driver combinations
	// Key: "modelID@driverID" to ensure uniqueness
	var modelList []*reliantv1.ModelInfo

	for _, model := range allModels {
		// For each provider the model supports, check if user has that provider configured
		for _, provider := range model.Providers {
			if provider.Driver == "reliant" && !models.CanDriverUseModel("reliant", models.ModelID(model.ID)) {
				continue
			}

			// Local models are never in the registry: they come from the user's
			// daemons and are appended below.
			if provider.Driver == string(local.Family) {
				continue
			}
			driverConfig, exists := availableDrivers.Drivers[models.DriverID(provider.Driver)]
			if !exists || !driverConfig.Enabled || driverConfig.APIKey == "" {
				continue
			}

			// Per-account availability: a dynamic provider (e.g. Copilot) may
			// report a model as disabled for this account (it 400s upstream),
			// so we hide it from the picker. Static providers report every
			// model enabled, and the lookup fails open (model absent -> shown),
			// so this uniformly gates any provider without a special-case.
			if byModel, ok := enabledByDriver[provider.Driver]; ok {
				if enabled, present := byModel[model.ID]; present && !enabled {
					continue
				}
			}

			driverID := provider.Driver

			// Create a unique ID that includes the driver
			// Format: "claude-4.5-sonnet@openrouter"
			uniqueID := model.ID + "@" + driverID

			// Get display name for the driver (used for grouping in UI)
			driverDisplayName := getDriverDisplayName(driverID)

			// The model as THIS driver serves it to this account, so the window
			// and compaction thresholds are the ones a request will run with.
			served := registry.ServedDefinition(model, driverID)
			defaultCompaction, maxCompaction := compactionThresholds(&served, driverID)

			modelList = append(modelList, &reliantv1.ModelInfo{
				Id:                         uniqueID,
				Name:                       model.Name,
				Provider:                   driverDisplayName, // For UI grouping
				DriverId:                   driverID,          // For routing
				Capabilities:               capabilitiesToStrings(model.Capabilities),
				ContextWindow:              int64(models.EffectiveContextWindow(&served, driverID)),
				DefaultMaxTokens:           int64(models.EffectiveMaxOutputTokens(&served, driverID)),
				CanReason:                  model.Capabilities.CanReason,
				SupportsAttachments:        model.Capabilities.SupportsAttachments,
				Tags:                       registry.TagsOf(model.ID),
				SupportsTools:              model.Capabilities.SupportsTools,
				SupportsCaching:            model.Capabilities.SupportsCaching,
				SupportedThinkingLevels:    models.SupportedThinkingLevels(model.Capabilities),
				SupportsTemperature:        models.SupportsTemperature(model, driverID),
				DefaultCompactionThreshold: defaultCompaction,
				MaxCompactionThreshold:     maxCompaction,
			})
		}
	}

	localModels := s.pickerLocalModels(ctx, scope)
	modelList = append(modelList, localModelInfos(localModels)...)

	// Sort models: by provider, then by model priority within each provider
	sortModelsByProvider(modelList)

	logging.DebugContext(ctx, "[ListModels] Returning models", "count", len(modelList))

	resp := &reliantv1.ListModelsResponse{
		Models: modelList,
		Total:  int32(len(modelList)),
		Tiers:  tierResolutions(registry, configuredProviderIDs(availableDrivers), s.loadTagPrefs(ctx, userID), s.loadProviderPreference(ctx, userID), localModels),
	}
	return connect.NewResponse(resp), nil
}

// pickerLocalModels is the caller's local models a chat of scope may run on:
// none when the org's policy refuses local models, as the driver build would
// at the call (drivers.CheckOrgPolicy).
func (s *Service) pickerLocalModels(ctx context.Context, scope drivers.Scope) []local.Model {
	if !scope.Personal() && drivers.OrgPolicyRefuses(ctx, scope, models.DriverID(local.Family), models.FundingLocal) {
		return nil
	}
	return s.listLocalModels(ctx, scope.UserID)
}

// configuredProviderIDs returns the driver IDs the user has properly
// configured. Mirrors the LLM-call handler's provider set so a tier shown here
// is the tier a request would actually resolve to.
func configuredProviderIDs(availableDrivers models.AvailableDrivers) []string {
	providers := make([]string, 0, len(availableDrivers.Drivers))
	for driverID, driverConfig := range availableDrivers.Drivers {
		if driverConfig.IsConfigured() {
			providers = append(providers, string(driverID))
		}
	}
	return providers
}

func (s *Service) loadTagPrefs(ctx context.Context, userID string) map[string]modelprefs.TagPrefs {
	prefs, err := modelprefs.LoadAll(ctx, s.deps.TagPrefs, userID)
	if err != nil {
		logging.WarnContext(ctx, "[ListModels] Model tag preferences partially unreadable", "error", err)
	}
	return prefs
}

func (s *Service) loadProviderPreference(ctx context.Context, userID string) modelprefs.ProviderPreference {
	pref, err := modelprefs.LoadProviderPreference(ctx, s.deps.TagPrefs, userID)
	if err != nil {
		logging.WarnContext(ctx, "[ListModels] Default provider preference unreadable; tiers use the built-in order", "error", err)
	}
	return pref
}

// sortModelsByProvider sorts models grouped by provider, with model priority within each group
// Uses centralized priority from models.yaml order
func sortModelsByProvider(modelList []*reliantv1.ModelInfo) {
	// Provider display order
	providerOrder := map[string]int{
		"Reliant":    1,
		"Anthropic":  2,
		"OpenAI":     3,
		"Codex":      4,
		"Google AI":  5,
		"OpenRouter": 6,
		"Local":      7,
	}

	reg := models.MustGetRegistry()
	sort.Slice(modelList, func(i, j int) bool {
		mi, mj := modelList[i], modelList[j]

		// First sort by provider
		pi := providerOrder[mi.Provider]
		pj := providerOrder[mj.Provider]
		if pi == 0 {
			pi = 99
		}
		if pj == 0 {
			pj = 99
		}
		if pi != pj {
			return pi < pj
		}

		// Within same provider, sort by model family then model priority
		// Extract base model ID (before @)
		miBase := extractBaseModelID(mi.Id)
		mjBase := extractBaseModelID(mj.Id)

		// Sort by model family
		fi := models.FamilyPriority[models.GetModelFamily(miBase)]
		fj := models.FamilyPriority[models.GetModelFamily(mjBase)]
		if fi == 0 {
			fi = 99
		}
		if fj == 0 {
			fj = 99
		}
		if fi != fj {
			return fi < fj
		}

		// Within same family, sort by model priority (from YAML order)
		miPriority := reg.GetModelPriority(miBase)
		mjPriority := reg.GetModelPriority(mjBase)
		if miPriority != mjPriority {
			return miPriority < mjPriority
		}

		// Fall back to alphabetical
		return mi.Name < mj.Name
	})
}
