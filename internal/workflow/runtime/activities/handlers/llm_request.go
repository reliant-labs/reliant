// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/modelprefs"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// This file is the single request-construction path for LLM calls made by
// workflow activities. CallLLM (the normal agent turn) and Compact (context
// summarization) both build their request envelope here, so provider
// constraints — thinking configuration, temperature gating, tool-less history
// sanitization, deterministic driver/provider selection — are enforced in
// exactly one place instead of being re-derived per activity.

// llmCallSpec describes an LLM call to resolve through the standard path.
type llmCallSpec struct {
	UserID    string
	SessionID string

	// OrgID and Attended scope the accounts the call may draw on (see
	// scope). Both come from the chat row the activity loaded, on the
	// server, through chatOrgID and chatAttended: never from workflow inputs,
	// a client or a daemon, which are replayed and inherited and so are not
	// authority. A call with no chat leaves them unset: the personal scope.
	OrgID    string
	Attended bool
	// ChatID is the chat the call is for: whose funding question (§10) it
	// reads and writes. Empty for a call with no chat.
	ChatID string
	// TurnStart reports that the call begins a turn (its history ends with a
	// person's or an agent's message, not a tool result). Only a turn start
	// moves a thread up to a better tier (§4.5, D6).
	TurnStart bool

	// UnattendedAnySource lifts the org-funded-only rule of an unattended run
	// whose automation may use any source (drivers.Scope.UnattendedAnySource),
	// read through chatUnattendedAnySource.
	UnattendedAnySource bool

	// Selector picks the model via the registry. Ignored when an injected
	// resolver (tests) supplies its own driver/model, mirroring CallLLM.
	Selector models.ModelSelector

	// FallbackSelector, when set, is tried only if Selector cannot resolve at
	// all against the user's providers. This is a STRICT ladder, unlike a
	// multi-tag Selector: tag scoring SUMS the weights of every selector tag a
	// model is listed under, so [fast, moderate] prefers a model carrying both
	// over one carrying only fast — which is the opposite of "fast, or moderate
	// if there is no fast model". Use this when the second tier is a genuine
	// fallback rather than a secondary preference.
	FallbackSelector models.ModelSelector

	// ExcludeProviders are drivers resolution must not pick, as if the user
	// had not connected them. Only titling sets it, to move a title off a
	// provider whose every account is limited; a chat turn keeps its provider
	// and leaves the decision to the workflow's opt-in failover.
	ExcludeProviders []string

	// RunProviders are the provider(s) the user chose for the run
	// (RuntimeContext.ProviderPreference). They apply only to a selector that
	// names no provider of its own (see withRunProviders), and then rank above
	// the user's tag preference model and the tier's own order.
	RunProviders []string
	// ChatProvider is a strict chat-wide selection, not a ranked preference.
	ChatProvider      string
	ClearProviderPins bool
	Descendant        bool
	PresetModelTags   []string
	// ChatModel is the chat's own model, what a descendant whose selector the
	// chat provider cannot serve runs on instead (chatModelFallback), unless
	// ThreadModelChosen: the user picked the descendant's model themselves.
	ChatModel         models.ModelSelector
	ThreadModelChosen bool

	// ThreadID is the thread the call belongs to. It is what keeps a thread on
	// one account of its provider (prompt cache and reasoning envelopes are per
	// account). Empty = no stickiness: the call takes the default account.
	ThreadID string

	// Routing picks the account of the resolved provider and benches one that
	// is refused. nil routes nothing (one-shot calls, tests).
	Routing *AccountRouting

	// Failover is the run's policy for a provider whose accounts are all
	// limited, and the accounts it prefers (RuntimeContext.Failover).
	Failover types.FailoverPolicy

	// Explicit per-call overrides. Zero values mean "use the resolved model's
	// default" — exactly like an unset workflow arg on a normal request.
	Temperature   *float64
	ThinkingLevel string
	MaxTokens     *int64
	WorkingDir    string

	// ForceToolChoice pins the request to a single named tool, making a tool
	// call the only thing the model can emit.
	//
	// Safe only for a ONE-SHOT request, never an agent loop: the pin applies to
	// every request in a turn, so a model given real tools alongside it could
	// never call them.
	ForceToolChoice string

	// TagPrefsReader, when set, layers the user's Settings → Model
	// preferences (model.tag_config.<tag>) under Selector/Temperature/
	// ThinkingLevel: node arg > model value > tag pref > tier/model default.
	// Only the chat-turn path sets it. Compaction and title generation pick
	// their own internal selectors ("cheap"/"fast" housekeeping tiers) and
	// must not inherit a user's per-tier temperature, thinking effort or
	// compaction threshold, nor be redirected to an expensive model_id the
	// user chose for conversation. Ignored when a resolver is injected.
	TagPrefsReader modelprefs.SettingsReader

	// Local, when set, lets a selector name a model served by one of the
	// user's daemons ("<name>@local", provider "local:<daemonID>"). Local
	// models never come from the registry: they are synthesized per request
	// from the daemons' published inventories.
	Local *LocalModelSpec
}

// scope is the drivers.Scope every account read of this call uses: the
// registry's candidates, the router's accounts, the default-account fallback
// and the failover's alternatives, so none of them can pick from a different
// set. A call with no org (no chat, or a chat that predates orgs) gets the
// person's own accounts.
func (s llmCallSpec) scope() drivers.Scope {
	if s.OrgID == "" {
		return drivers.PersonalScope(s.UserID)
	}
	return drivers.Scope{UserID: s.UserID, OrgID: s.OrgID, Attended: s.Attended, UnattendedAnySource: s.UnattendedAnySource}
}

// chatOrgID is the org a call made for chat is scoped to: the chat row's
// org_id, "" when the chat has none (or there is no chat).
func chatOrgID(chat *db.Chat) string {
	if chat == nil || chat.OrgID == nil {
		return ""
	}
	return *chat.OrgID
}

// AttendanceStore is what deciding who is behind a chat's call reads
// (chatAttendance): the chat rows of an agent.start_run lineage, its launch
// events, and whether a person has taken a turn in a chat.
type AttendanceStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetTriggerEventByChat(ctx context.Context, kind core.TriggerEventKind, chatID string) (*core.TriggerEvent, error)
	// ChatPersonTurnAt is when a person last sent a message into the chat,
	// nil when nobody has.
	ChatPersonTurnAt(ctx context.Context, chatID string) (*time.Time, error)
	// GetTrigger reads the trigger that launched an unattended run, for its
	// unattended-funding stamp.
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
}

// attendance is who is behind a call made for a chat (org-provider-accounts
// §11.1): attended runs follow the chat rules (§10), unattended ones draw on
// org-funded sources only, unless anySource (§11.3).
type attendance struct {
	attended  bool
	anySource bool
}

// attendanceVerdict is what deciding attendance concluded: a person is
// behind the run, it was resolved as an automation's, or it could not be
// decided at all.
type attendanceVerdict int

const (
	verdictAttended attendanceVerdict = iota
	// verdictUnattended: resolved, a stored trigger launched the run (or the
	// root of its agent lineage).
	verdictUnattended
	// verdictUnknown: a read failed, there was no store, an agent-started
	// run had no launch event or parent, or the lineage was broken or too
	// deep to walk.
	verdictUnknown
)

// maxAttendanceLineage bounds the agent.start_run walk; the fork-bomb guard
// (tools.MaxAgentStartRunDepth) keeps real lineages far shorter.
const maxAttendanceLineage = 8

// chatAttendance decides, on the server and per call, whether a person is
// behind a call made for chat (§11.1):
//
//   - I2, per turn: a chat a person has sent a message into (adopting an
//     automation's chat) is attended from then on, with its sub-agents; each
//     trigger fire is a chat of its own, so no later trigger turn shares it.
//   - a run a stored trigger launched (core.TriggerEventKind.Unattended) is
//     unattended, a hand fire of the trigger included;
//   - I1: a run an agent started (agent.start_run) is what its lineage root
//     is: the parent_chat_id walk the fork-bomb guard does;
//   - every other run (chat.start, manual.run, cli.run, builder.test, a chat
//     that predates launch events), and a call with no chat, is attended.
//
// A resolved unattended run draws on what its trigger's unattended-funding
// stamp allows (§11.3, I4): every source for "any" and for a trigger that
// predates the stamp (NULL, today's behaviour), the org's own for
// "org_only". A run whose attendance could not be decided, or whose
// trigger cannot be read, is unattended with anySource false: org-funded
// sources only, never the person's own (fail closed).
func chatAttendance(ctx context.Context, store AttendanceStore, chat *db.Chat) attendance {
	verdict, root := attendedFrom(ctx, store, chat)
	switch verdict {
	case verdictAttended:
		return attendance{attended: true}
	case verdictUnattended:
		return attendance{anySource: triggerMaySpendAnySource(ctx, store, root)}
	default:
		return attendance{}
	}
}

// triggerMaySpendAnySource reads the unattended-funding stamp of the trigger
// that launched root, a resolved unattended run (§11.3, I4): any, or a trigger
// that predates the stamp (NULL), may draw on every source; org_only may not.
// A trigger that cannot be read, or one deleted since it fired (the launch
// keeps its kind, not its trigger), gets the org's sources only (fail closed).
func triggerMaySpendAnySource(ctx context.Context, store AttendanceStore, root *db.Chat) bool {
	if root == nil || root.TriggerID == nil {
		return false
	}
	t, err := store.GetTrigger(ctx, *root.TriggerID)
	if err != nil || t == nil {
		logging.WarnContext(ctx, "Could not read the trigger's unattended funding; using only the org's sources", "triggerID", *root.TriggerID, "error", err)
		return false
	}
	return !t.UnattendedFunding.OrgOnly()
}

func chatAttended(ctx context.Context, store AttendanceStore, chat *db.Chat) attendanceVerdict {
	verdict, _ := attendedFrom(ctx, store, chat)
	return verdict
}

// attendedFrom is chatAttended, with the chat the verdict was read from: for
// a resolved unattended run, the chat the stored trigger launched (the
// lineage root of an agent-started run).
func attendedFrom(ctx context.Context, store AttendanceStore, chat *db.Chat) (attendanceVerdict, *db.Chat) {
	for range maxAttendanceLineage {
		if chat == nil {
			return verdictAttended, nil
		}
		launched := core.TriggerEventKind(chat.LaunchKind)
		// The launch's kind decides, never whether its trigger still
		// exists: deleting a trigger clears its launch events' trigger_id,
		// and must not turn its runs into attended ones.
		triggered := launched.Unattended()
		if !triggered && launched != core.TriggerEventKindAgentStartRun {
			return verdictAttended, nil
		}
		if store == nil {
			return verdictUnknown, nil
		}
		at, err := store.ChatPersonTurnAt(ctx, chat.ID)
		if err != nil {
			logging.WarnContext(ctx, "Could not read whether a person took a turn in the chat; using only the org's sources", "chatID", chat.ID, "error", err)
			return verdictUnknown, nil
		}
		if at != nil {
			return verdictAttended, nil
		}
		if triggered {
			return verdictUnattended, chat
		}
		// agent.start_run: the lineage root decides.
		event, err := store.GetTriggerEventByChat(ctx, core.TriggerEventKindAgentStartRun, chat.ID)
		if err != nil {
			logging.WarnContext(ctx, "Could not read an agent-started run's launch; using only the org's sources", "chatID", chat.ID, "error", err)
			return verdictUnknown, nil
		}
		parent, _ := event.Payload["parent_chat_id"].(string)
		if parent == "" {
			logging.WarnContext(ctx, "An agent-started run names no parent; using only the org's sources", "chatID", chat.ID)
			return verdictUnknown, nil
		}
		if chat, err = store.GetChat(ctx, parent); err != nil {
			logging.WarnContext(ctx, "Could not read an agent-started run's parent chat; using only the org's sources", "chatID", parent, "error", err)
			return verdictUnknown, nil
		}
	}
	logging.WarnContext(ctx, "An agent-started run's lineage is too deep to walk; using only the org's sources")
	return verdictUnknown, nil
}

// LocalModelSpec is what resolving a local model needs beyond the selector.
// Exported so the dev probe can supply an in-process relay.
type LocalModelSpec struct {
	Directory local.Directory
	Transport local.TransportFactory
	// PreferDaemonID is the chat's worktree daemon, tried first when the
	// selector does not pin a daemon.
	PreferDaemonID string
	// Custom reaches user-configured endpoints (model_endpoints); nil leaves
	// them unavailable on this worker.
	Custom *local.CustomRoutes
}

// resolvedLLMCall bundles the driver plus the effective settings the shared
// request path computed for it.
type resolvedLLMCall struct {
	Driver llm.Driver

	// Model is the resolved model (probed from the driver when a resolver is
	// injected).
	Model models.Model

	// ModelID is the model ID handed to driver resolution, including the
	// @driver suffix when resolved via the registry (e.g.
	// "claude-4.6-sonnet@anthropic").
	ModelID string

	// Definition is the registry definition backing the model; nil when an
	// injected resolver supplied the model.
	Definition *models.ModelDefinition

	// ProviderDriver is the driver of the provider the registry selected to
	// serve this call (e.g. "codex", "anthropic"). Empty when an injected
	// resolver supplied the model. Context management derives the model's REAL
	// window from this provider, since a provider can serve a smaller window
	// than the model-wide capability (see models.EffectiveContextWindow).
	ProviderDriver string

	// ThinkingLevel is the effective, capability-reconciled thinking level.
	// Empty means the model cannot reason and the driver omits thinking.
	ThinkingLevel string

	// Temperature is the effective temperature (explicit override or model
	// default), nil when neither is set.
	Temperature *float64

	// Account is the account of ProviderDriver that serves this call; nil when
	// the call was not routed (local, injected resolver, a provider with no
	// accounts, routing off).
	Account *routedAccount

	// OrgID is the org whose work the call was (llmCallSpec.OrgID): the org
	// its usage row is recorded against.
	OrgID string

	// FundingNotice is a transcript notice the call owes about who pays for
	// it (the person's own account in place of the org's, or the org's
	// again); "" when none.
	FundingNotice string

	// plan is the funding plan the call was routed under, for the refusal
	// path's question (§10).
	plan fundingPlan

	// TagCompactionThreshold is the tag preference's compaction threshold, 0
	// when none applied. The caller uses it only when neither the node arg
	// nor the model value set one.
	TagCompactionThreshold int32
}

// promptCeiling is the largest prompt this call may send: the context budget
// compaction, the pin cap, the trim backstop and the stored-token-count sanity
// check all derive from (models.PromptCeiling). def is nil when an injected
// resolver supplied model.
func promptCeiling(def *models.ModelDefinition, providerDriver string, model models.Model) int64 {
	if def != nil {
		return int64(models.ProviderPromptCeiling(def, providerDriver))
	}
	return int64(models.PromptCeiling(int(model.ContextWindow), int(model.DefaultMaxTokens)))
}

// resolveLLMCall resolves a spec into a ready-to-use driver the same way for
// every activity:
//   - registry resolution over the user's configured providers (the provider
//     is pinned via the id@driver preference, so selection is deterministic)
//   - model-default temperature and thinking level layered under explicit
//     overrides, then reconciled against the model's actual capabilities
//   - standard driver options (session, working directory, max tokens,
//     reasoning effort)
//
// When resolver is non-nil (injected, e.g. in tests) the registry is skipped
// and the model is probed from the resolver, matching CallLLM's behavior.
//
// On a bound chat, a descendant whose own selector the chat's provider cannot
// serve runs on the chat's model (chatModelFallback) rather than holding.
func resolveLLMCall(ctx context.Context, resolver drivers.DriverResolver, spec llmCallSpec) (*resolvedLLMCall, error) {
	res, err := resolveLLMCallOnce(ctx, resolver, spec)
	if err == nil || !isProviderSwitchHold(err) {
		return res, err
	}
	fallback, ok := chatModelFallback(spec)
	if !ok {
		return nil, err
	}
	res, fallbackErr := resolveLLMCallOnce(ctx, resolver, fallback)
	if fallbackErr != nil {
		// The thread's own selector is what the user can act on, so the
		// hold that names it is the one reported.
		logging.WarnContext(ctx, "[LLMRequest] Chat model cannot run on the chat's provider either; holding the thread",
			"chatProvider", spec.ChatProvider, "chatModel", spec.ChatModel.ID, "chatModelTags", spec.ChatModel.Tags, "error", fallbackErr)
		return nil, err
	}
	logging.InfoContext(ctx, "[LLMRequest] Descendant's model cannot run on the chat's provider; running on the chat's model",
		"chatProvider", spec.ChatProvider,
		"ownModel", spec.Selector.ID, "ownTags", spec.Selector.Tags, "ownProviders", spec.Selector.Providers,
		"chatModel", res.ModelID, "threadID", spec.ThreadID)
	return res, nil
}

func resolveLLMCallOnce(ctx context.Context, resolver drivers.DriverResolver, spec llmCallSpec) (*resolvedLLMCall, error) {
	var legacyModel models.Model
	var definition *models.ModelDefinition
	var modelIDForDriver string
	var providerDriver string
	var localTransport http.RoundTripper
	var routingDrivers models.AvailableDrivers
	// scopeDrivers is the scope as drivers.GetDriver reads it (no funding
	// plan applied): what its default account is.
	var scopeDrivers models.AvailableDrivers
	var plan fundingPlan
	var fundingNotice string
	effectiveTemperature := spec.Temperature
	effectiveThinkingLevel := spec.ThinkingLevel
	var tagCompactionThreshold int32

	// A selector that names no provider takes the run's: the user's choice of
	// provider for the run outranks the user's per-tag model preference and
	// the tier's own order. runPinned records that it did.
	selector, runPinned := withRunProviders(spec.Selector, spec.RunProviders)
	// What a hold reports: the thread's own selector, not the probe that
	// pins it to the chat's provider (a "<model>@<chat provider>" that the
	// provider was never going to serve).
	ownSelector := spec.Selector
	if spec.ChatProvider != "" {
		selector = switchSelector(spec)
		spec.Selector = selector
		runPinned = true
	}

	if resolver != nil {
		// Probe the injected resolver for its model. Explicit spec values are
		// used directly (no model defaults available), but are still
		// reconciled against the probed model's capabilities so non-reasoning
		// drivers don't receive unsupported reasoning options.
		probeDriver, probeErr := resolver(ctx, spec.UserID, nil)
		if probeErr != nil {
			return nil, fmt.Errorf("injected driver resolver failed: %w", probeErr)
		}
		legacyModel = probeDriver.Model()
		modelIDForDriver = string(legacyModel.ID)
		effectiveThinkingLevel = models.ReconcileThinkingLevel(
			models.ResolveThinkingCapability(models.ModelCapabilities{CanReason: legacyModel.CanReason}),
			effectiveThinkingLevel,
		)
	} else if local.IsSelector(spec.Selector) {
		if spec.ChatProvider != "" && spec.ChatProvider != "local" {
			return nil, providerSwitchHold(spec.ChatProvider, ownSelector, fmt.Errorf("an explicit local model cannot run on a registry provider"))
		}
		var err error
		legacyModel, definition, modelIDForDriver, effectiveThinkingLevel, localTransport, err = resolveLocalModel(ctx, spec)
		if err != nil {
			return nil, err
		}
		providerDriver = string(local.Family)
	} else if pinned, pinnedSpec, ok := resolvePinnedLocalPref(ctx, spec); ok && !runPinned {
		legacyModel, definition, modelIDForDriver, effectiveThinkingLevel, localTransport = pinned.legacy, pinned.definition, pinned.modelID, pinned.thinkingLevel, pinned.transport
		providerDriver = string(local.Family)
		if effectiveTemperature == nil {
			effectiveTemperature = pinnedSpec.Temperature
		}
		if pinnedSpec.TagCompactionThreshold > 0 {
			tagCompactionThreshold = pinnedSpec.TagCompactionThreshold
		}
	} else {
		// Resolve model using the registry against the providers the user has
		// configured.
		// A store failure says nothing about what the user has connected, so it
		// must not read as "connect a provider", which is terminal.
		availableDrivers, lerr := lookupAvailableDriversWithRetry(ctx, spec.scope())
		if lerr != nil {
			return nil, lerr
		}
		// Who pays (§4.4, §10): the person's order for the org, a lease they
		// granted, and the thread kept on its provider mid-turn.
		scopeDrivers = availableDrivers
		plan = spec.Routing.fundingPlanFor(ctx, spec, availableDrivers)
		availableDrivers = spec.Routing.applyFundingPlan(ctx, spec, plan, availableDrivers)
		routingDrivers = availableDrivers
		availableProviders := slices.DeleteFunc(configuredProviderIDs(availableDrivers), func(p string) bool {
			return slices.Contains(spec.ExcludeProviders, p)
		})
		if spec.ChatProvider != "" {
			// Providers on ModelSelector are preference-ranked. Restrict the
			// eligible set as well so neither tag fallback nor user tag prefs
			// can silently send this call back to the previous provider.
			availableProviders = nil
			if cfg, ok := availableDrivers.Drivers[models.DriverID(spec.ChatProvider)]; ok && cfg.IsConfigured() {
				availableProviders = []string{spec.ChatProvider}
			}
		}

		// The driver rank is the weakest rule: among the drivers serving the
		// model resolution picks, the one with the run's org-paid account wins
		// (org-provider-accounts §4.3). It never changes the model.
		registry := models.MustGetRegistry().WithAvailability(availableDrivers.Availability)
		if availableDrivers.DriverRank != nil {
			registry = registry.WithDriverRank(availableDrivers.TierFor)
		}
		resolve := registry.Resolve
		if len(spec.FallbackSelector.Tags) > 0 || spec.FallbackSelector.ID != "" {
			resolve = func(selector models.ModelSelector, providers []string) (*models.ResolvedModel, error) {
				return registry.ResolveWithFallback(selector, spec.FallbackSelector, providers)
			}
		}

		prefs := loadTagPrefs(ctx, spec, registry)
		var resolved *models.ResolvedModel
		var runProviders []string
		if runPinned {
			runProviders = selector.Providers
		}
		if preferred := prefs.PreferredModelOn(registry, selector, availableProviders, runProviders); preferred != nil {
			resolved = preferred
		} else {
			if prefs.ModelID != "" && !prefs.IsLocalModel() && !runPinned {
				logging.WarnContext(ctx, "[LLMRequest] Ignoring tag preference model_id: not available for this user",
					"userID", spec.UserID, "tag", modelprefs.TagFor(spec.Selector), "modelID", prefs.ModelID)
			}
			var err error
			resolved, err = resolve(selector, availableProviders)
			if err != nil {
				if spec.ChatProvider != "" {
					return nil, providerSwitchHold(spec.ChatProvider, ownSelector, err)
				}
				return nil, resolutionError(err, availableDrivers)
			}
		}
		if effectiveTemperature == nil {
			effectiveTemperature = prefs.Temperature
		}
		if effectiveThinkingLevel == "" {
			effectiveThinkingLevel = prefs.ValidThinkingLevel()
		}
		if prefs.CompactionThreshold != nil && *prefs.CompactionThreshold > 0 {
			tagCompactionThreshold = *prefs.CompactionThreshold
		}

		if spec.ChatProvider != "" {
			if resolved.Provider.Driver != spec.ChatProvider {
				return nil, providerSwitchHold(spec.ChatProvider, ownSelector, fmt.Errorf("resolution selected a different provider %s", resolved.Provider.Driver))
			}
			if selector.ID != "" {
				id, _, _ := strings.Cut(selector.ID, "@")
				if resolved.Definition.ID != id {
					return nil, providerSwitchHold(spec.ChatProvider, ownSelector, fmt.Errorf("the exact model is retired or unavailable; choose its replacement explicitly"))
				}
			}
		}
		resolvedDef := resolved.Definition
		definition = &resolvedDef

		// Apply model defaults for unset parameters.
		if effectiveTemperature == nil {
			effectiveTemperature = resolvedDef.DefaultTemperature
		}
		// Thinking level precedence, highest first:
		//   1. explicit node/preset/request thinking_level (spec.ThinkingLevel)
		//   2. the registry's answer: the winning tag entry's level (clamped to
		//      the model), or the model's capability default when chosen by id
		//      — see ModelRegistry.Resolve
		if effectiveThinkingLevel == "" {
			effectiveThinkingLevel = resolved.ThinkingLevel
		}
		if effectiveThinkingLevel != "" {
			tl := ThinkingLevel(effectiveThinkingLevel)
			if !tl.IsValid() {
				return nil, fmt.Errorf("invalid thinking_level: %s (must be one of: %s)", tl, strings.Join(models.KnownThinkingLevels, ", "))
			}
		}
		// Reconcile through the canonical model capability policy so stale
		// defaults on non-reasoning models disable thinking instead of
		// failing before a request can be made.
		effectiveThinkingLevel = models.ReconcileThinkingLevel(
			models.ResolveThinkingCapability(resolvedDef.Capabilities),
			effectiveThinkingLevel,
		)

		// Build the model ID string with driver suffix so driver resolution
		// uses the exact provider the registry picked (deterministic).
		modelIDForDriver = resolvedDef.ID
		providerDriver = resolved.Provider.Driver
		if resolved.Provider.Driver != "" {
			modelIDForDriver = resolvedDef.ID + "@" + resolved.Provider.Driver
		}

		// The model as the chosen provider serves it, so driver.Model() reports
		// that provider's window and max output, not the model-wide figures.
		legacyModel = resolvedDef.ToModel()
		legacyModel.ContextWindow = int64(models.EffectiveContextWindow(&resolvedDef, providerDriver))
		legacyModel.DefaultMaxTokens = int64(models.EffectiveMaxOutputTokens(&resolvedDef, providerDriver))
	}

	// Build preferences for driver selection.
	preferences := models.Preferences{
		{
			ModelID:     models.ModelID(modelIDForDriver),
			Temperature: effectiveTemperature,
		},
	}

	// Standard driver options shared by every request.
	driverOpts := []llm.DriverOption{
		llm.WithModel(legacyModel),
		llm.WithSessionID(spec.SessionID),
	}
	if spec.WorkingDir != "" {
		driverOpts = append(driverOpts, llm.WithWorkingDirectory(spec.WorkingDir))
	}
	if spec.MaxTokens != nil {
		driverOpts = append(driverOpts, llm.WithMaxTokens(*spec.MaxTokens))
	}
	if effectiveThinkingLevel != "" {
		driverOpts = append(driverOpts, llm.WithReasoningEffort(effectiveThinkingLevel))
	}
	if spec.ForceToolChoice != "" {
		driverOpts = append(driverOpts, llm.WithForceToolChoice(spec.ForceToolChoice))
	}

	var driver llm.Driver
	var account *routedAccount
	if localTransport == nil && resolver == nil && providerDriver != "" && !drivers.ReplayMode.Enabled {
		var rerr error
		upgrade := spec.TurnStart || (plan.question != nil && plan.question.State != core.PersonalFundingUsePersonal)
		account, rerr = routeAccount(ctx, spec, routingDrivers, providerDriver, definitionID(definition), upgrade)
		if rerr != nil {
			// Every account is limited: refuse here, with no provider call, the
			// way the provider would have, carrying the facts to decide on.
			return nil, spec.Routing.usageLimitFailure(ctx, rerr, usageLimitContext{
				Scope: spec.scope(), ThreadID: spec.ThreadID, Driver: models.DriverID(providerDriver),
				ModelID: definitionID(definition), Policy: spec.Failover,
				ChatID: spec.ChatID, Plan: plan,
				OrgFundedLimited: limitedTierIsOrgFunded(routingDrivers.Accounts[models.DriverID(providerDriver)], definitionID(definition)),
			})
		}
		// Before any provider call: does this call move the chat from the
		// org's accounts to the person's own without their say (§10.1)?
		var ferr error
		if fundingNotice, ferr = spec.Routing.checkPersonalFunding(ctx, spec, plan, routingDrivers, account, providerDriver, definitionID(definition)); ferr != nil {
			return nil, ferr
		}
	}
	if localTransport != nil {
		driverOpts = append(driverOpts,
			llm.WithBaseURL(local.PlaceholderBaseURL),
			llm.WithTransport(localTransport),
			llm.WithMaxTokens(int64(definition.Capabilities.MaxOutputTokens)))
		if effectiveTemperature != nil {
			driverOpts = append(driverOpts, llm.WithTemperature(*effectiveTemperature))
		}
		if spec.MaxTokens != nil {
			driverOpts = append(driverOpts, llm.WithMaxTokens(*spec.MaxTokens))
		}
		var err error
		if driver, err = drivers.NewLocalDriver(legacyModel, driverOpts...); err != nil {
			return nil, fmt.Errorf("failed to get LLM driver: %w", err)
		}
	} else {
		if account != nil {
			// The re-check at the driver build (org-provider-accounts §8.1 point
			// 3): an account of another org never serves this run. Refused, the
			// call falls to the default account of the same scope, never fails.
			if serr := drivers.CheckAccountScope(ctx, spec.scope(), account.Config.DriverConfig); serr != nil {
				account = nil
			}
		}
		if account != nil {
			if d, aerr := spec.Routing.DriverFor(ctx, spec.UserID, account.Config.DriverConfig, legacyModel, append(drivers.PreferenceDriverOptions(legacyModel, preferences[0]), driverOpts...)...); aerr != nil {
				// A driver for the chosen account that cannot be built is not
				// worth a failed turn: the provider's default account serves.
				logging.WarnContext(ctx, "[LLMRequest] Could not build a driver for the thread's account; using the provider's default account",
					"userID", spec.UserID, "provider", providerDriver, "accountID", account.Config.AccountID, "error", aerr)
				account = nil
			} else {
				driver = d
			}
		}
		if driver == nil && resolver == nil && providerDriver != "" && !drivers.ReplayMode.Enabled {
			// No routed account serves: the provider's default account will,
			// and it answers to the same consent (§10).
			notice, ferr := spec.Routing.checkDefaultAccountFunding(ctx, spec, plan, scopeDrivers, routingDrivers, providerDriver, definitionID(definition))
			if ferr != nil {
				return nil, ferr
			}
			if notice != "" {
				fundingNotice = notice
			}
		}
		if driver == nil {
			var err error
			if resolver != nil {
				// An injected resolver (tests) reads no accounts.
				driver, err = resolver(ctx, spec.UserID, preferences, driverOpts...)
			} else {
				// The provider's default account, from the same scope the
				// router saw.
				driver, err = drivers.GetDriver(ctx, spec.scope(), preferences, driverOpts...)
			}
			if err != nil {
				return nil, fmt.Errorf("failed to get LLM driver: %w", err)
			}
		}
	}

	return &resolvedLLMCall{
		Driver:         driver,
		Model:          legacyModel,
		ModelID:        modelIDForDriver,
		Definition:     definition,
		ProviderDriver: providerDriver,
		Account:        account,
		ThinkingLevel:  effectiveThinkingLevel,
		Temperature:    effectiveTemperature,
		OrgID:          spec.OrgID,
		FundingNotice:  fundingNotice,
		plan:           plan,

		TagCompactionThreshold: tagCompactionThreshold,
	}, nil
}

type pinnedLocalPref struct {
	legacy        models.Model
	definition    *models.ModelDefinition
	modelID       string
	thinkingLevel string
	transport     http.RoundTripper
}

// pinnedLocalSpec carries the pref-derived values the local path needs.
type pinnedLocalSpec struct {
	Temperature            *float64
	TagCompactionThreshold int32
}

// resolvePinnedLocalPref applies a tag preference whose model_id is a local
// model. It reports ok=false (so the caller falls back to the tier) when
// there is no such preference or its machine cannot serve it right now.
func resolvePinnedLocalPref(ctx context.Context, spec llmCallSpec) (pinnedLocalPref, pinnedLocalSpec, bool) {
	prefs := loadTagPrefs(ctx, spec, nil)
	if !prefs.IsLocalModel() {
		return pinnedLocalPref{}, pinnedLocalSpec{}, false
	}
	localSpec := spec
	localSpec.Selector = prefs.LocalSelector()
	if localSpec.ThinkingLevel == "" {
		localSpec.ThinkingLevel = prefs.ValidThinkingLevel()
	}
	legacy, def, modelID, thinking, transport, err := resolveLocalModel(ctx, localSpec)
	if err != nil {
		logging.WarnContext(ctx, "[LLMRequest] Ignoring tag preference model_id: local model unavailable; using the tier",
			"userID", spec.UserID, "tag", modelprefs.TagFor(spec.Selector), "modelID", prefs.ModelID, "error", err)
		return pinnedLocalPref{}, pinnedLocalSpec{}, false
	}
	out := pinnedLocalSpec{Temperature: prefs.Temperature}
	if prefs.CompactionThreshold != nil && *prefs.CompactionThreshold > 0 {
		out.TagCompactionThreshold = *prefs.CompactionThreshold
	}
	return pinnedLocalPref{legacy, def, modelID, thinking, transport}, out, true
}

// loadTagPrefs returns the user's preference for the selector's tier, or the
// zero value when there is none, the selector is id-based, no reader is
// wired, or the settings read fails (a preference must never block a call).
func loadTagPrefs(ctx context.Context, spec llmCallSpec, registry *models.ModelRegistry) modelprefs.TagPrefs {
	tag := modelprefs.TagFor(spec.Selector)
	if tag == "" || spec.TagPrefsReader == nil {
		return modelprefs.TagPrefs{}
	}
	all, err := modelprefs.LoadAll(ctx, spec.TagPrefsReader, spec.UserID)
	if err != nil {
		logging.WarnContext(ctx, "[LLMRequest] Model tag preferences partially unreadable; affected tags use defaults",
			"userID", spec.UserID, "error", err)
	}
	return all[tag]
}

// resolveFailure is a model resolution that failed, worded for the user. Its
// cause stays in the chain, so a no-servable-provider failure remains
// errors.Is(drivererrors.ErrNoServableProvider) and the runtime fails the call
// once instead of retrying a resolution only the user can change.
type resolveFailure struct {
	msg   string
	cause error
}

func (e *resolveFailure) Error() string { return e.msg }
func (e *resolveFailure) Unwrap() error { return e.cause }

// resolutionError says why no provider can serve the request and what the user
// can do about it. A model pinned to one provider ("gpt-5.6-sol@codex", which
// is what picking a model in the composer stores) names that provider and why
// it cannot serve: not connected, or connected with a credential the provider
// rejected. Anything else names every rejected provider, since a rejected
// credential is the likeliest reason a configured user's tier came up empty.
func resolutionError(err error, available models.AvailableDrivers) error {
	var pinned *models.ProviderUnavailableError
	if errors.As(err, &pinned) {
		return &resolveFailure{msg: "failed to resolve model: " + pinned.Explain(available.Unavailable), cause: err}
	}
	msg := "failed to resolve model: " + err.Error()
	if len(available.Unavailable) > 0 {
		reasons := make([]string, 0, len(available.Unavailable))
		for _, reason := range available.Unavailable {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		msg += ". " + strings.Join(reasons, "; ") + ". Reconnect it in Settings → Providers, or connect another provider"
	} else {
		msg += ". Connect a provider that serves this model in Settings → Providers"
	}
	return &resolveFailure{msg: msg, cause: err}
}

// withRunProviders gives a model selector that names no provider the
// provider(s) the user chose for the run (RuntimeContext.ProviderPreference).
//
// A selector that already names a provider — a pinned "<model>@<driver>" id or
// its own providers list — is the more specific choice and is returned
// unchanged. For a tier selector the providers rank the tier's candidates
// (ModelRegistry.Resolve); where they serve none, the tier's own order
// decides, so a preference never makes a call unresolvable. The bool reports
// whether the run's providers were applied.
func withRunProviders(selector models.ModelSelector, runProviders []string) (models.ModelSelector, bool) {
	if len(runProviders) == 0 || len(selector.Providers) > 0 || strings.Contains(selector.ID, "@") {
		return selector, false
	}
	selector.Providers = append([]string(nil), runProviders...)
	return selector, true
}

// configuredProviderIDs returns the driver IDs the user has properly
// configured (API key drivers and local drivers with a BaseURL).
func configuredProviderIDs(availableDrivers models.AvailableDrivers) []string {
	providers := make([]string, 0, len(availableDrivers.Drivers))
	for driverID, driverConfig := range availableDrivers.Drivers {
		if driverConfig.IsConfigured() {
			providers = append(providers, string(driverID))
		}
	}
	return providers
}

// prepareHistoryForLLM applies the standard provider-safety transforms every
// outgoing request needs, in one place:
//
//  1. Trim history to fit the context window, accounting for system prompts
//     and tool definitions. The backstop threshold is derived from the model's
//     real context window (contextWindow) so it scales per-model and sits above
//     the compaction threshold; pass 0 when the window is unknown to fall back
//     to the fixed legacy threshold.
//  2. Tool-less requests: flatten tool_use/tool_result content blocks to plain
//     text. Providers (Anthropic directly and via LiteLLM, OpenAI, ...) reject
//     histories that carry tool-call blocks when the request has no tools
//     param, so any request sent without tools (the compaction summarization
//     call, a call_llm node with tools disabled) must not carry them.
//  3. Normalize internal roles (agent -> user) to API-compatible roles.
//
// TRIM BEFORE FLATTEN (load-bearing ordering): the trim's only mechanism for
// freeing real volume is trimLargeToolResults, which cuts ToolResult parts
// larger than 10k chars. Flattening first rewrites every ToolResult into a
// TextContent, so the trim would find nothing to cut, free nothing, and still
// report success — shipping an oversized request the provider rejects. Trimming
// first lets the backstop see the tool results it is designed to shrink; the
// flatten then runs on the already-trimmed history and still guarantees no
// tool-call blocks survive on a tool-less request.
func prepareHistoryForLLM(chatID string, history []message.Message, systemPrompts []string, availableTools []tools.Tool, contextWindow int64) []message.Message {
	if message.TrimMessagesToFitContextWindow(history, systemPrompts, wrapToolsForEstimation(availableTools), contextWindow) {
		logging.Debug("[LLMRequest] Trimmed history to fit context window",
			"chatID", chatID,
			"contextWindow", contextWindow)
	}
	if len(availableTools) == 0 {
		history = flattenToolContentToText(history)
	}
	return normalizeRolesForLLM(history)
}

// flattenToolContentToText rewrites a conversation into text-only messages
// suitable for a request that passes no tools.
//
// tool_use and tool_result content blocks are converted to human-readable text
// so the model still sees what tools were called and what they returned,
// without carrying tool-call content blocks that providers reject when no
// `tools=` param is set. Assistant tool calls collapse into the assistant
// turn; tool-result messages become user turns (the only non-system role that
// reliably accepts free text across providers).
func flattenToolContentToText(messages []message.Message) []message.Message {
	flattened := make([]message.Message, 0, len(messages))

	for _, msg := range messages {
		var sb strings.Builder
		for _, tc := range msg.TextContents() {
			if tc.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(tc.Text)
			}
		}

		for _, call := range msg.ToolCalls() {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			fmt.Fprintf(&sb, "[Called tool %s with input: %s]", call.Name, call.Input)
		}

		role := msg.Role
		for _, result := range msg.ToolResults() {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			status := "result"
			if result.IsError {
				status = "error"
			}
			fmt.Fprintf(&sb, "[Tool %s %s: %s]", result.Name, status, result.Content)
			// Tool messages have no valid free-text role of their own; fold into the user turn.
			role = message.User
		}

		text := sb.String()
		if strings.TrimSpace(text) == "" {
			// Nothing summarizable (e.g. a bare finish marker); drop it.
			continue
		}

		flattened = append(flattened, message.Message{
			Role:   role,
			Hidden: msg.Hidden,
			Parts:  []message.ContentPart{message.TextContent{Text: text}},
		})
	}

	return flattened
}

// resolveLocalModel resolves a "<name>@local" selector against the inventories
// of the user's daemons. Failures are *local.UnavailableError, worded for the
// user; they are not API-key problems and are never reported as one.
func resolveLocalModel(ctx context.Context, spec llmCallSpec) (legacy models.Model, def *models.ModelDefinition, modelID, thinkingLevel string, transport http.RoundTripper, err error) {
	if spec.Local == nil || spec.Local.Directory == nil {
		return legacy, nil, "", "", nil, &local.UnavailableError{
			Model:  strings.TrimSuffix(spec.Selector.ID, "@"+local.IDSuffix),
			Reason: "local models are not reachable from this server",
		}
	}
	all, err := local.ListModels(ctx, spec.Local.Directory, spec.UserID)
	if err != nil {
		return legacy, nil, "", "", nil, fmt.Errorf("failed to list local models: %w", err)
	}
	picked, err := local.Resolve(all, spec.Selector, spec.Local.PreferDaemonID)
	if err != nil {
		return legacy, nil, "", "", nil, err
	}

	definition := picked.Definition
	// A local model thinks only when asked and only at a level its server
	// takes; no tag or capability default applies, since an unprompted level
	// can 400 on a model that offers none.
	if spec.ThinkingLevel != "" {
		thinkingLevel = models.ReconcileThinkingLevel(models.ResolveThinkingCapability(definition.Capabilities), spec.ThinkingLevel)
	}
	transport, err = local.TransportFor(ctx, spec.UserID, picked, spec.Local.Transport, spec.Local.Custom)
	if err != nil {
		return legacy, nil, "", "", nil, err
	}
	return definition.ToModel(), &definition, picked.CatalogID(), thinkingLevel, transport, nil
}

// definitionID is the registry id of the resolved model, "" for a call that did
// not resolve through the registry.
func definitionID(def *models.ModelDefinition) string {
	if def == nil {
		return ""
	}
	return def.ID
}
