package skills

import (
	"context"
	"fmt"
	"strings"

	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	skillmaterialize "github.com/reliant-labs/reliant/internal/skills/materialize"
	skillprompt "github.com/reliant-labs/reliant/internal/skills/prompt"
)

// ResolveTurnInput captures all inputs needed to compute skill context for one user turn.
type ResolveTurnInput struct {
	ProjectPath               string
	LatestUserMessage         string
	RecentUserMessages        []string
	SupportingLimits          SupportingFilesLimits
	ActivationMode            string
	IntegrationMode           string
	RetrievalConfig           RetrievalConfig
	DisabledDefinitionPathSet map[string]struct{}
	AvailableSkillsLimits     AvailableSkillsRenderLimits
	AvailableToolNames        []string
}

type SkillInvocationTrigger string

type SkillInvocationStatus string

const (
	SkillInvocationTriggerExplicit SkillInvocationTrigger = "explicit"
	SkillInvocationTriggerAuto     SkillInvocationTrigger = "auto"

	SkillInvocationStatusActivated SkillInvocationStatus = "activated"
	SkillInvocationStatusFailed    SkillInvocationStatus = "failed"
	SkillInvocationStatusSkipped   SkillInvocationStatus = "skipped"
)

// SkillInvocation captures structured lifecycle metadata for frontend rendering.
type SkillInvocation struct {
	Trigger       SkillInvocationTrigger
	Status        SkillInvocationStatus
	RequestedName string
	SkillName     string
	Message       string
	Warnings      []string
}

// ResolveTurnResult is the fully materialized skill context for prompt integration.
type ResolveTurnResult struct {
	Discovered             Result
	ActiveSkill            *Skill
	ActiveSkillSection     string
	Notices                []Notice
	WarningHints           []string
	AvailableSkillsSection string
	AllowedToolNames       []string
	Invocation             *SkillInvocation
}

// RetrievalConfig defines ranking/chunking/prompt-budget controls for active skill context.
type RetrievalConfig = skillmaterialize.RetrievalConfig

type buildContextInput struct {
	SupportingLimits SupportingFilesLimits
	RetrievalConfig  RetrievalConfig
	QueryText        string
	IntegrationMode  skillmaterialize.IntegrationMode
}

// DiscoverInput captures metadata discovery inputs for settings/services that need snapshots.
type DiscoverInput struct {
	ProjectPath               string
	DisabledDefinitionPathSet map[string]struct{}
	LoadFullDefinitions       bool
}

type catalog interface {
	discover(ctx context.Context, input DiscoverInput) Result
}

type skillSelector interface {
	resolveActiveSkill(discovered Result, input activationInput) (*Skill, []Notice)
}

type skillActivator interface {
	buildActiveSkillContext(active Skill, input buildContextInput) (Skill, []Diagnostic)
}

type sectionRenderer interface {
	buildAvailableSkillsSection(skills []Skill, limits AvailableSkillsRenderLimits, mode skillmaterialize.IntegrationMode) string
	buildActiveSkillSection(skill Skill) string
	warningHints(notices []Notice) []string
}

type toolPolicyEngine interface {
	filterAllowedToolNames(activeSkill *Skill, availableToolNames []string) ([]string, []Notice)
}

type runtime interface {
	ResolveTurn(ctx context.Context, input ResolveTurnInput) ResolveTurnResult
	Discover(ctx context.Context, input DiscoverInput) Result
}

type runtimeOptions struct {
	catalog        catalog
	selector       skillSelector
	activator      skillActivator
	promptRenderer sectionRenderer
	policyEngine   toolPolicyEngine
}

type runtimeImpl struct {
	catalog        catalog
	selector       skillSelector
	activator      skillActivator
	promptRenderer sectionRenderer
	policyEngine   toolPolicyEngine
}

type indexedCatalog struct{}

type activationSelector struct{}

type supportingFilesActivator struct{}

type promptSectionRenderer struct{}

var defaultRuntime runtime = newRuntime(runtimeOptions{})

// DefaultRuntime returns the process-wide skills runtime.
func DefaultRuntime() runtime {
	return defaultRuntime
}

func newRuntime(opts runtimeOptions) runtime {
	catalogImpl := opts.catalog
	if catalogImpl == nil {
		catalogImpl = indexedCatalog{}
	}

	selectorImpl := opts.selector
	if selectorImpl == nil {
		selectorImpl = activationSelector{}
	}

	activatorImpl := opts.activator
	if activatorImpl == nil {
		activatorImpl = supportingFilesActivator{}
	}

	rendererImpl := opts.promptRenderer
	if rendererImpl == nil {
		rendererImpl = promptSectionRenderer{}
	}

	policyImpl := opts.policyEngine
	if policyImpl == nil {
		policyImpl = allowedToolsPolicyEngine{}
	}

	return runtimeImpl{
		catalog:        catalogImpl,
		selector:       selectorImpl,
		activator:      activatorImpl,
		promptRenderer: rendererImpl,
		policyEngine:   policyImpl,
	}
}

// ResolveTurn resolves discovery, activation, notices, and prompt sections for one turn.
func (r runtimeImpl) ResolveTurn(ctx context.Context, input ResolveTurnInput) ResolveTurnResult {
	limits := skillscore.NormalizeSupportingFilesLimits(input.SupportingLimits)
	retrievalCfg := skillmaterialize.NormalizeRetrievalConfig(input.RetrievalConfig)
	integrationMode := skillmaterialize.NormalizeIntegrationMode(input.IntegrationMode)

	discovered := r.catalog.discover(ctx, DiscoverInput{
		ProjectPath:               input.ProjectPath,
		DisabledDefinitionPathSet: input.DisabledDefinitionPathSet,
		LoadFullDefinitions:       false,
	})

	active, activationNotices := r.selector.resolveActiveSkill(discovered, activationInput{
		LatestUserMessage:  input.LatestUserMessage,
		RecentUserMessages: input.RecentUserMessages,
		ActivationMode:     input.ActivationMode,
	})
	explicitInvocation := extractExplicitInvocation(input.LatestUserMessage) != ""

	notices := append([]Notice(nil), activationNotices...)

	var activeSkill *Skill
	if active != nil {
		selected, diagnostics := r.activator.buildActiveSkillContext(*active, buildContextInput{
			SupportingLimits: limits,
			RetrievalConfig:  retrievalCfg,
			QueryText:        buildTurnQuery(input.LatestUserMessage, input.RecentUserMessages),
			IntegrationMode:  integrationMode,
		})
		activeSkill = &selected
		if explicitInvocation {
			notices = append(notices, skillmaterialize.BuildActiveSkillNotice(skillToActiveSkill(selected), diagnostics))
		}
	}

	if len(discovered.Diagnostics) > 0 {
		notices = append(notices, Notice{
			Level:   NoticeLevelWarning,
			Message: fmt.Sprintf("%d skill file(s) had validation/discovery errors and were skipped.", len(discovered.Diagnostics)),
		})
	}

	activeSkillSection := ""
	if activeSkill != nil {
		activeSkillSection = r.promptRenderer.buildActiveSkillSection(*activeSkill)
	}

	filteredToolNames, policyNotices := r.policyEngine.filterAllowedToolNames(activeSkill, input.AvailableToolNames)
	notices = append(notices, policyNotices...)

	invocation := buildSkillInvocation(input.LatestUserMessage, input.ActivationMode, activeSkill, notices)

	return ResolveTurnResult{
		Discovered:             discovered,
		ActiveSkill:            activeSkill,
		ActiveSkillSection:     activeSkillSection,
		Notices:                notices,
		WarningHints:           r.promptRenderer.warningHints(notices),
		AvailableSkillsSection: r.promptRenderer.buildAvailableSkillsSection(discovered.Skills, input.AvailableSkillsLimits, integrationMode),
		AllowedToolNames:       append([]string(nil), filteredToolNames...),
		Invocation:             invocation,
	}
}

func buildSkillInvocation(latestUserMessage, activationMode string, activeSkill *Skill, notices []Notice) *SkillInvocation {
	requested := extractExplicitInvocation(latestUserMessage)
	trigger := SkillInvocationTriggerAuto
	if strings.TrimSpace(requested) != "" {
		trigger = SkillInvocationTriggerExplicit
	}

	status := SkillInvocationStatusSkipped
	if activeSkill != nil {
		status = SkillInvocationStatusActivated
	} else if trigger == SkillInvocationTriggerExplicit {
		status = SkillInvocationStatusFailed
	}

	if trigger == SkillInvocationTriggerAuto && strings.EqualFold(strings.TrimSpace(activationMode), "explicit") {
		return nil
	}
	if trigger == SkillInvocationTriggerAuto && activeSkill == nil {
		return nil
	}
	if trigger == SkillInvocationTriggerExplicit && strings.TrimSpace(requested) == "" {
		return nil
	}

	warningMessages := make([]string, 0)
	for _, notice := range notices {
		if notice.Level == NoticeLevelWarning && strings.TrimSpace(notice.Message) != "" {
			warningMessages = append(warningMessages, notice.Message)
		}
	}

	invocation := &SkillInvocation{
		Trigger:       trigger,
		Status:        status,
		RequestedName: requested,
		Warnings:      warningMessages,
	}
	if activeSkill != nil {
		invocation.SkillName = activeSkill.Name
		invocation.Message = fmt.Sprintf("Activated skill %s", activeSkill.Name)
	} else if requested != "" {
		invocation.Message = fmt.Sprintf("Requested skill %s was not activated", requested)
	}
	if invocation.RequestedName == "" {
		invocation.RequestedName = invocation.SkillName
	}
	return invocation
}

// Discover returns a discovered-skills snapshot for service surfaces.
func (r runtimeImpl) Discover(ctx context.Context, input DiscoverInput) Result {
	return r.catalog.discover(ctx, input)
}

func snapshotToResult(in skillcatalog.Snapshot) Result {
	out := Result{
		Skills:       make([]Skill, 0, len(in.Definitions)),
		ByName:       make(map[string]Skill, len(in.ByName)),
		Diagnostics:  append([]Diagnostic(nil), in.Diagnostics...),
		ShadowedBy:   make(map[string]string, len(in.ShadowedBy)),
		ShadowedFrom: make(map[string]string, len(in.ShadowedFrom)),
	}
	for _, definition := range in.Definitions {
		out.Skills = append(out.Skills, definitionToSkill(definition))
	}
	for key, definition := range in.ByName {
		out.ByName[key] = definitionToSkill(definition)
	}
	for key, value := range in.ShadowedBy {
		out.ShadowedBy[key] = value
	}
	for key, value := range in.ShadowedFrom {
		out.ShadowedFrom[key] = value
	}
	return out
}

func (indexedCatalog) discover(ctx context.Context, input DiscoverInput) Result {
	return snapshotToResult(skillcatalog.DefaultCatalogIndex().Discover(ctx, skillcatalog.DiscoverInput{
		ProjectPath:               input.ProjectPath,
		DisabledDefinitionPathSet: input.DisabledDefinitionPathSet,
		LoadFullDefinitions:       input.LoadFullDefinitions,
	}))
}

func (activationSelector) resolveActiveSkill(discovered Result, input activationInput) (*Skill, []Notice) {
	return resolveActiveSkill(discovered, input)
}

func (supportingFilesActivator) buildActiveSkillContext(active Skill, input buildContextInput) (Skill, []Diagnostic) {
	result := skillmaterialize.BuildActiveSkillContext(skillToDefinition(active), skillmaterialize.BuildInput{
		SupportingLimits: input.SupportingLimits,
		RetrievalConfig:  input.RetrievalConfig,
		QueryText:        input.QueryText,
		IntegrationMode:  input.IntegrationMode,
	}, skillcatalog.LoadFullDefinition)
	return activeSkillToSkill(result.Skill), result.Diagnostics
}

func (promptSectionRenderer) buildAvailableSkillsSection(skills []Skill, limits AvailableSkillsRenderLimits, mode skillmaterialize.IntegrationMode) string {
	opts := skillprompt.AvailableSkillsRenderOptions{}
	if mode == skillmaterialize.IntegrationModeTool {
		opts.OmitLocations = true
	}
	return skillprompt.BuildAvailableSkillsSection(skillSliceToDefinitions(skills), limits, opts)
}

func (promptSectionRenderer) buildActiveSkillSection(skill Skill) string {
	return skillprompt.BuildSelectedSkillSection(skillToActiveSkill(skill))
}

func (promptSectionRenderer) warningHints(notices []Notice) []string {
	return skillprompt.WarningHintsForPrompt(notices)
}
