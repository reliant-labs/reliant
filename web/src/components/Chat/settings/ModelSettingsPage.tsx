import { useState, useMemo } from "react";
import { useNavigate } from "@tanstack/react-router";
import { ChevronLeft, X, Search, Settings2 } from "lucide-react";
import { cn } from "../../../lib/utils";
import { useModels } from "../../../store/globalDataStore";
import { Tooltip } from "../../ui/Tooltip";
import {
  contextSeverity,
  contextWarningText,
  formatContextWindow,
  localGroupLabel,
  localProviderRef,
} from "../../Settings/localModels";
import { preferredThinkingLevel, resolveThinkingCapabilityForModel } from "../../../hooks/useThinkingCapability";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ModelSettingsPageProps {
  value: unknown; // { tags: [...] }, { id: "..." }, or { tags: [...], temperature: 0.4, thinking_level: "high", ... }
  onChange: (value: unknown) => void;
  onBack: () => void;
  onClose: () => void;
}

type ModelTab = "tag" | "explicit";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const TAGS = ["powerful", "flagship", "moderate", "fast", "cheap"] as const;

const tagDescriptions: Record<string, string> = {
  powerful: "Maximum capability — the hardest work",
  flagship: "Best reasoning — complex tasks",
  moderate: "Balanced — implementation, review",
  fast: "Speed optimized — research, simple tasks",
  cheap: "Lowest cost — bulk, internal",
};

const providerColors: Record<string, string> = {
  anthropic: "#e8945a",
  openai: "#5cb85c",
  codex: "#5cb85c",
  gemini: "#5b9bd5",
  vertexai: "#5b9bd5",
  local: "#a0a0a0",
  openrouter: "#f0ad4e",
};

const ALL_THINKING_LEVELS = [
  { value: "", label: "Auto" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "X-High" },
  { value: "max", label: "Max" },
];

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function getProviderColor(driverIdOrProvider: string | undefined): string {
  if (!driverIdOrProvider) return "#a0a0a0";
  const key = driverIdOrProvider.toLowerCase();
  return providerColors[key] ?? "#a0a0a0";
}

/** Extract typed fields from the opaque model value object. */
function parseModelValue(value: unknown): {
  tags?: string[];
  id?: string;
  providers?: string[];
  thinking_level?: string;
  temperature?: number;
  compaction_threshold?: number;
} {
  if (!value || typeof value !== "object") return {};
  return value as Record<string, unknown>;
}

/** Build a new value by merging base selection with overrides. */
function buildModelValue(
  base: { tags?: string[]; id?: string; providers?: string[] },
  overrides: Record<string, unknown>,
): unknown {
  const result: Record<string, unknown> = { ...base };
  for (const [key, val] of Object.entries(overrides)) {
    if (val !== undefined && val !== null && val !== "") {
      result[key] = val;
    }
  }
  return result;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ModelSettingsPage({
  value,
  onChange,
  onBack,
  onClose,
}: ModelSettingsPageProps) {
  const navigate = useNavigate();
  const parsed = parseModelValue(value);

  // Determine initial tab from current value
  const initialTab: ModelTab = parsed.id ? "explicit" : "tag";
  const [activeTab, setActiveTab] = useState<ModelTab>(initialTab);
  const [searchQuery, setSearchQuery] = useState("");

  const { models, tiers } = useModels();

  // Current overrides extracted from value
  const currentThinking = parsed.thinking_level ?? "";
  const currentTemperature = parsed.temperature;
  const currentCompaction = parsed.compaction_threshold;

  // Find which tag is selected
  const selectedTag = parsed.tags?.[0] ?? null;

  // Find which explicit model is selected
  const selectedModelId = parsed.id ?? null;

  // Resolve each tag to the model the server says it runs on for this user
  // (tiers). The first-model-with-tag guess is only a fallback for when tiers
  // have not loaded — the list is provider-sorted, not resolution-ordered.
  const tagResolvedModels = useMemo(() => {
    const result: Record<string, (typeof models)[number] | null> = {};
    for (const tag of TAGS) {
      const tierModelId = tiers?.[tag]?.modelId;
      result[tag] =
        (tierModelId
          ? models.find((m) => m.id === tierModelId)
          : models.find((m) => m.tags?.includes(tag))) ?? null;
    }
    return result;
  }, [models, tiers]);

  // Group models by provider for explicit tab
  const groupedModels = useMemo(() => {
    const filtered = searchQuery
      ? models.filter(
          (m) =>
            m.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
            m.id.toLowerCase().includes(searchQuery.toLowerCase()) ||
            m.provider.toLowerCase().includes(searchQuery.toLowerCase()),
        )
      : models;

    const groups: Record<string, typeof models> = {};
    for (const model of filtered) {
      // A local model is reachable only through one machine's daemon, so each
      // machine is its own group.
      const provider = model.local
        ? localGroupLabel(model.local.machineName)
        : model.provider || "Other";
      if (!groups[provider]) groups[provider] = [];
      groups[provider].push(model);
    }
    return groups;
  }, [models, searchQuery]);

  const providers = useMemo(
    () => Object.keys(groupedModels).sort(),
    [groupedModels],
  );

  // Get the currently selected model (for showing defaults in overrides)
  const selectedModel = useMemo(() => {
    if (selectedModelId) {
      return (
        models.find((m) => m.id === selectedModelId) ??
        models.find((m) => m.id.split("@")[0] === selectedModelId) ??
        null
      );
    }
    if (selectedTag) {
      return tagResolvedModels[selectedTag] ?? null;
    }
    return null;
  }, [selectedModelId, selectedTag, models, tagResolvedModels]);

  // A tag's tier owns its effort (server-resolved); "Auto" runs at this level.
  const tierThinking =
    !selectedModelId && selectedTag ? tiers?.[selectedTag]?.thinkingLevel || null : null;

  // The level "Auto" actually runs at: the tier's effort for a tag, otherwise
  // the capability default the server applies to a model chosen by id.
  const modelDefaultThinking = useMemo(() => {
    if (tierThinking) return tierThinking;
    const levels = selectedModel?.supportedThinkingLevels;
    if (!levels || levels.length === 0) return null;
    return preferredThinkingLevel(resolveThinkingCapabilityForModel(selectedModel.id, [selectedModel]).levels);
  }, [selectedModel, tierThinking]);

  // Build the thinking levels available for the current model
  // Always include Auto (""), plus only the levels the model supports
  const availableThinkingLevels = useMemo(() => {
    const supported = selectedModel?.supportedThinkingLevels;
    if (!supported || supported.length === 0) return ALL_THINKING_LEVELS.slice(0, 1); // Auto only
    return ALL_THINKING_LEVELS.filter(
      (l) => l.value === "" || supported.includes(l.value)
    );
  }, [selectedModel]);

  // The largest compaction_threshold the server honors for the selected
  // model+driver; undefined when it reports none.
  const maxCompaction = selectedModel?.maxCompactionThreshold || undefined;
  const compactionCapped =
    maxCompaction !== undefined && currentCompaction !== undefined && currentCompaction > maxCompaction;

  // Build override object (only non-default/non-auto values)
  const currentOverrides: Record<string, unknown> = {};
  if (currentThinking) currentOverrides.thinking_level = currentThinking;
  if (currentTemperature !== undefined)
    currentOverrides.temperature = currentTemperature;
  if (currentCompaction !== undefined)
    currentOverrides.compaction_threshold = currentCompaction;

  // Handler: change base selection.
  //
  // Overrides that mean the same thing on any model (temperature, a thinking
  // level the new model supports) carry over. compaction_threshold does not:
  // it is an absolute token count whose meaning depends on the window of the
  // model it was set for — the field's own placeholder is that model's
  // default — so it belongs to the selection, and switching drops it. Keeping
  // it "when it still fits" is not checkable here: a tag re-resolves on the
  // server, so the model this page shows for it may not be the one that runs.
  // Prod incident 2026-10-09: a 1M threshold set on one model rode a switch
  // onto a smaller one, compaction never fired, and the trim backstop
  // shredded the conversation instead.
  const handleSelectTag = (tag: string) => {
    const resolvedModel = tagResolvedModels[tag];
    const supportedLevels = resolvedModel?.supportedThinkingLevels ?? [];
    const newOverrides = { ...currentOverrides };
    if (newOverrides.thinking_level && !supportedLevels.includes(newOverrides.thinking_level as string)) {
      delete newOverrides.thinking_level;
    }
    if (selectedModelId || selectedTag !== tag) {
      delete newOverrides.compaction_threshold;
    }
    onChange(buildModelValue({ tags: [tag] }, newOverrides));
  };

  const handleSelectModel = (modelId: string, daemonId?: string) => {
    const newModel = models.find((m) => m.id === modelId && (!daemonId || m.local?.daemonId === daemonId)) ?? models.find((m) => m.id.split("@")[0] === modelId);
    const newLevels = newModel?.supportedThinkingLevels ?? [];
    // Clear thinking_level if the new model doesn't support the current level
    const newOverrides = { ...currentOverrides };
    if (newOverrides.thinking_level && !newLevels.includes(newOverrides.thinking_level as string)) {
      delete newOverrides.thinking_level;
    }
    const sameModel =
      (selectedModelId === modelId || modelId.split("@")[0] === selectedModelId) &&
      (parsed.providers ?? []).join(",") === (daemonId ? localProviderRef(daemonId) : "");
    if (!sameModel) {
      delete newOverrides.compaction_threshold;
    }
    onChange(
      buildModelValue(
        daemonId ? { id: modelId, providers: [localProviderRef(daemonId)] } : { id: modelId },
        newOverrides,
      ),
    );
  };

  // Handler: change overrides
  const handleOverrideChange = (
    key: string,
    val: string | number | undefined,
  ) => {
    const base: { tags?: string[]; id?: string; providers?: string[] } = {};
    if (parsed.tags) base.tags = parsed.tags;
    if (parsed.id) base.id = parsed.id;
    if (parsed.id && parsed.providers) base.providers = parsed.providers;

    const newOverrides = { ...currentOverrides };
    if (val === undefined || val === "" || val === null) {
      delete newOverrides[key];
    } else {
      newOverrides[key] = val;
    }
    onChange(buildModelValue(base, newOverrides));
  };

  return (
    <div className="flex flex-col">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-2.5 border-b border-border/50">
        <button
          onClick={onBack}
          className="w-6 h-6 flex items-center justify-center rounded hover:bg-muted/50 text-muted-foreground hover:text-foreground transition-colors"
        >
          <ChevronLeft className="w-3.5 h-3.5" />
        </button>
        <h3 className="text-sm font-semibold text-foreground flex-1">
          Model
        </h3>
        <button
          onClick={() => {
            onClose();
            navigate({ to: '/settings/$section', params: { section: 'general' } });
          }}
          title="Model preferences"
          className="w-6 h-6 flex items-center justify-center rounded text-muted-foreground hover:text-foreground transition-colors"
        >
          <Settings2 className="w-3.5 h-3.5" />
        </button>
        <button
          onClick={onClose}
          className="w-6 h-6 flex items-center justify-center rounded text-muted-foreground hover:text-foreground transition-colors"
        >
          <X className="w-3.5 h-3.5" />
        </button>
      </div>

      {/* Tab bar */}
      <div className="flex border-b border-border/50 px-3">
        <button
          onClick={() => setActiveTab("tag")}
          className={cn(
            "px-3 py-2 text-xs font-medium border-b-2 transition-colors bg-transparent",
            activeTab === "tag"
              ? "text-primary border-primary"
              : "text-muted-foreground border-transparent hover:text-foreground",
          )}
        >
          By Tag
        </button>
        <button
          onClick={() => setActiveTab("explicit")}
          className={cn(
            "px-3 py-2 text-xs font-medium border-b-2 transition-colors bg-transparent",
            activeTab === "explicit"
              ? "text-primary border-primary"
              : "text-muted-foreground border-transparent hover:text-foreground",
          )}
        >
          Explicit
        </button>
      </div>

      {/* Tab content */}
      {activeTab === "tag" && (
        <div className="p-2">
          {TAGS.map((tag) => {
            const resolved = tagResolvedModels[tag];
            const isSelected = selectedTag === tag;
            return (
              <button
                key={tag}
                onClick={() => handleSelectTag(tag)}
                className={cn(
                  "w-full flex items-center justify-between px-2.5 py-2 rounded-md cursor-pointer transition-colors mb-0.5 text-left",
                  isSelected
                    ? "bg-primary/15 outline outline-1 outline-primary/25"
                    : "hover:bg-muted/50",
                )}
              >
                <div className="flex flex-col gap-px">
                  <span className="text-sm font-semibold text-foreground capitalize">
                    {tag}
                  </span>
                  <span className="text-xs text-muted-foreground/70">
                    {tagDescriptions[tag]}
                  </span>
                </div>
                {resolved && (
                  <div className="flex items-center gap-1 text-xs text-muted-foreground shrink-0">
                    <span
                      className="w-1.5 h-1.5 rounded-full inline-block shrink-0"
                      style={{
                        backgroundColor: getProviderColor(
                          resolved.driverId || resolved.provider,
                        ),
                      }}
                    />
                    {resolved.name}
                  </div>
                )}
              </button>
            );
          })}
        </div>
      )}

      {activeTab === "explicit" && (
        <div>
          {/* Search */}
          <div className="px-2.5 pt-2">
            <div className="relative">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground/70" />
              <input
                type="text"
                placeholder="Search models..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="w-full pl-8 pr-2.5 py-1.5 bg-muted border border-border rounded text-foreground text-xs outline-none focus:border-border/80 placeholder:text-muted-foreground/70"
              />
            </div>
          </div>

          {/* Model list */}
          <div className="p-1 max-h-60 overflow-y-auto">
            {providers.map((provider) => (
              <div key={provider}>
                <div className="px-2.5 py-1.5 text-2xs font-semibold uppercase tracking-wider text-muted-foreground/70">
                  {provider}
                </div>
                {groupedModels[provider].map((model) => {
                  const local = model.local;
                  const isSelected = local
                    ? selectedModelId === model.id &&
                      parsed.providers?.includes(localProviderRef(local.daemonId)) === true
                    : selectedModelId === model.id ||
                      model.id.split("@")[0] === selectedModelId;
                  const offline = !!local && !local.online;
                  const row = (
                    <button
                      key={`${local?.daemonId ?? ""}:${model.id}`}
                      disabled={offline}
                      onClick={() => handleSelectModel(model.id, local?.daemonId)}
                      className={cn(
                        "w-full flex items-center justify-between px-2.5 py-1.5 rounded text-left transition-colors mx-0.5",
                        isSelected
                          ? "bg-primary/15"
                          : "hover:bg-muted/50",
                        offline && "opacity-50 cursor-not-allowed hover:bg-transparent",
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <span
                          className="w-1.5 h-1.5 rounded-full inline-block shrink-0"
                          style={{
                            backgroundColor: getProviderColor(
                              model.driverId || model.provider,
                            ),
                          }}
                        />
                        <span className="text-sm font-medium text-foreground">
                          {model.name}
                        </span>
                      </div>
                      <div className="flex items-center gap-1.5 text-2xs text-muted-foreground/70">
                        {local && !!model.contextWindow && (
                          <span
                            data-testid="local-context"
                            title={contextWarningText(model.contextWindow) || undefined}
                            className={cn(
                              contextSeverity(model.contextWindow) === "warning" && "text-warning font-semibold",
                              contextSeverity(model.contextWindow) === "soft" && "text-warning/80",
                            )}
                          >
                            {formatContextWindow(BigInt(model.contextWindow))}
                            {contextSeverity(model.contextWindow) === "warning" && " · too small for agents"}
                            {contextSeverity(model.contextWindow) === "soft" && " · small"}
                          </span>
                        )}
                        {model.canReason && (
                          <span className="inline-flex px-1 py-px rounded-sm text-3xs font-semibold uppercase tracking-tight bg-primary/15 text-primary">
                            reasoning
                          </span>
                        )}
                        {model.capabilities?.includes("fast") && (
                          <span className="inline-flex px-1 py-px rounded-sm text-3xs font-semibold uppercase tracking-tight bg-sky-400/15 text-sky-400">
                            fast
                          </span>
                        )}
                      </div>
                    </button>
                  );
                  return offline ? (
                    <Tooltip
                      key={`${local?.daemonId}:${model.id}`}
                      content={`${local?.machineName} is offline — this model is unavailable until it reconnects.`}
                      wrapperClassName="block w-full"
                    >
                      {row}
                    </Tooltip>
                  ) : (
                    row
                  );
                })}
              </div>
            ))}
            {providers.length === 0 && (
              <div className="px-4 py-6 text-center text-xs text-muted-foreground/70">
                {searchQuery
                  ? "No models match your search"
                  : "No models available"}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Overrides section */}
      <div className="border-t border-border/50 px-3.5 py-2.5">
        <div className="text-2xs font-semibold uppercase tracking-wider text-muted-foreground/70 mb-2">
          Overrides
        </div>

        {/* Thinking Level */}
        <div className="flex items-center justify-between mb-2">
          <span className="text-xs text-muted-foreground font-medium">
            Thinking
            {modelDefaultThinking && (
              <span className="text-2xs text-muted-foreground/70 font-normal">
                {" "}
                · default: {modelDefaultThinking}
              </span>
            )}
          </span>
          <select
            value={currentThinking}
            onChange={(e) =>
              handleOverrideChange("thinking_level", e.target.value || undefined)
            }
            className="px-2 py-1 bg-muted border border-border rounded text-foreground text-xs cursor-pointer outline-none hover:border-border/80"
          >
            {availableThinkingLevels.map((level) => (
              <option key={level.value} value={level.value}>
                {level.value === "" && tierThinking
                  ? `${level.label} (${tierThinking})`
                  : level.label}
              </option>
            ))}
          </select>
        </div>

        {/* Temperature: only where the model+driver honors it */}
        {selectedModel?.supportsTemperature !== false && (
        <div className="flex items-center justify-between mb-2" data-testid="temperature-control">
          <span className="text-xs text-muted-foreground font-medium">
            Temperature
          </span>
          <div className="flex items-center gap-2">
            <input
              type="range"
              min="0"
              max="100"
              value={
                currentTemperature !== undefined
                  ? Math.round(currentTemperature * 100)
                  : 100
              }
              onChange={(e) => {
                const val = Number(e.target.value) / 100;
                handleOverrideChange("temperature", val);
              }}
              className="w-20 h-1 appearance-none bg-border rounded cursor-pointer accent-primary"
            />
            <span className="text-xs text-muted-foreground min-w-7 text-right">
              {currentTemperature !== undefined
                ? currentTemperature.toFixed(1)
                : "Default"}
            </span>
          </div>
        </div>
        )}

        {/* Compaction: the placeholder is where this model+driver compacts by
            default; the max is the largest pin the server honors there (85% of
            the prompt ceiling) — a larger pin is capped to it. */}
        <div className="flex items-center justify-between">
          <span className="text-xs text-muted-foreground font-medium">
            Compaction
            {maxCompaction && (
              <span data-testid="compaction-max" className="text-2xs text-muted-foreground/70 font-normal">
                {" "}
                · max {maxCompaction.toLocaleString("en-US")}
              </span>
            )}
          </span>
          <input
            type="number"
            value={currentCompaction ?? ""}
            placeholder={String(selectedModel?.defaultCompactionThreshold || 185000)}
            max={maxCompaction}
            onChange={(e) => {
              const val = e.target.value
                ? Number(e.target.value)
                : undefined;
              handleOverrideChange("compaction_threshold", val);
            }}
            step={5000}
            className="w-[70px] px-2 py-1 bg-muted border border-border rounded text-foreground text-xs outline-none text-right focus:border-border/80"
          />
        </div>
        {compactionCapped && maxCompaction !== undefined && (
          <div data-testid="compaction-capped" className="mt-1 text-2xs text-warning text-right">
            Capped at {maxCompaction.toLocaleString("en-US")}: compaction must run before the model&apos;s prompt limit.
          </div>
        )}
      </div>
    </div>
  );
}