import { useState, useMemo } from "react";
import { ChevronLeft, X, Search, Settings2 } from "lucide-react";
import { cn } from "../../../lib/utils";
import { useModels } from "../../../store/globalDataStore";
import { useViewerStore } from "../../../store/viewerStore";

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

const TAGS = ["flagship", "moderate", "fast", "cheap"] as const;

const tagDescriptions: Record<string, string> = {
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
  xai: "#d9534f",
  local: "#a0a0a0",
  openrouter: "#f0ad4e",
};

const ALL_THINKING_LEVELS = [
  { value: "", label: "Auto" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "X-High" },
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
  thinking_level?: string;
  temperature?: number;
  compaction_threshold?: number;
} {
  if (!value || typeof value !== "object") return {};
  return value as Record<string, unknown>;
}

/** Build a new value by merging base selection with overrides. */
function buildModelValue(
  base: { tags?: string[]; id?: string },
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

function formatContextWindow(tokens: number | undefined): string {
  if (!tokens) return "";
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(tokens % 1_000_000 === 0 ? 0 : 1)}M`;
  if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}k`;
  return String(tokens);
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
  const parsed = parseModelValue(value);

  // Determine initial tab from current value
  const initialTab: ModelTab = parsed.id ? "explicit" : "tag";
  const [activeTab, setActiveTab] = useState<ModelTab>(initialTab);
  const [searchQuery, setSearchQuery] = useState("");

  const { models } = useModels();

  // Current overrides extracted from value
  const currentThinking = parsed.thinking_level ?? "";
  const currentTemperature = parsed.temperature;
  const currentCompaction = parsed.compaction_threshold;

  // Find which tag is selected
  const selectedTag = parsed.tags?.[0] ?? null;

  // Find which explicit model is selected
  const selectedModelId = parsed.id ?? null;

  // Resolve tags to first matching model
  const tagResolvedModels = useMemo(() => {
    const result: Record<string, (typeof models)[number] | null> = {};
    for (const tag of TAGS) {
      // Find first model whose tags include this tag
      result[tag] =
        models.find(
          (m) => m.tags?.includes(tag),
        ) ?? null;
    }
    return result;
  }, [models]);

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
      const provider = model.provider || "Other";
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

  // Determine the model's default thinking level from supportedThinkingLevels
  const modelDefaultThinking = useMemo(() => {
    const levels = selectedModel?.supportedThinkingLevels;
    if (!levels || levels.length === 0) return null;
    if (levels.includes("high")) return "high";
    return levels[levels.length - 1];
  }, [selectedModel]);

  // Build the thinking levels available for the current model
  // Always include Auto (""), plus only the levels the model supports
  const availableThinkingLevels = useMemo(() => {
    const supported = selectedModel?.supportedThinkingLevels;
    if (!supported || supported.length === 0) return ALL_THINKING_LEVELS.slice(0, 1); // Auto only
    return ALL_THINKING_LEVELS.filter(
      (l) => l.value === "" || supported.includes(l.value)
    );
  }, [selectedModel]);

  // Build override object (only non-default/non-auto values)
  const currentOverrides: Record<string, unknown> = {};
  if (currentThinking) currentOverrides.thinking_level = currentThinking;
  if (currentTemperature !== undefined)
    currentOverrides.temperature = currentTemperature;
  if (currentCompaction !== undefined)
    currentOverrides.compaction_threshold = currentCompaction;

  // Handler: change base selection
  const handleSelectTag = (tag: string) => {
    const resolvedModel = tagResolvedModels[tag];
    const supportedLevels = resolvedModel?.supportedThinkingLevels ?? [];
    const newOverrides = { ...currentOverrides };
    if (newOverrides.thinking_level && !supportedLevels.includes(newOverrides.thinking_level as string)) {
      delete newOverrides.thinking_level;
    }
    onChange(buildModelValue({ tags: [tag] }, newOverrides));
  };

  const handleSelectModel = (modelId: string) => {
    const newModel = models.find((m) => m.id === modelId) ?? models.find((m) => m.id.split("@")[0] === modelId);
    const newLevels = newModel?.supportedThinkingLevels ?? [];
    // Clear thinking_level if the new model doesn't support the current level
    const newOverrides = { ...currentOverrides };
    if (newOverrides.thinking_level && !newLevels.includes(newOverrides.thinking_level as string)) {
      delete newOverrides.thinking_level;
    }
    onChange(buildModelValue({ id: modelId }, newOverrides));
  };

  // Handler: change overrides
  const handleOverrideChange = (
    key: string,
    val: string | number | undefined,
  ) => {
    const base: { tags?: string[]; id?: string } = {};
    if (parsed.tags) base.tags = parsed.tags;
    if (parsed.id) base.id = parsed.id;

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
        <h3 className="text-[13px] font-semibold text-foreground flex-1">
          Model
        </h3>
        <button
          onClick={() => {
            onClose();
            useViewerStore.getState().setSettingsMode(true, 'general');
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
                  <span className="text-[13px] font-semibold text-foreground capitalize">
                    {tag}
                  </span>
                  <span className="text-[11px] text-muted-foreground/70">
                    {tagDescriptions[tag]}
                  </span>
                </div>
                {resolved && (
                  <div className="flex items-center gap-1 text-[11px] text-muted-foreground shrink-0">
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
                <div className="px-2.5 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">
                  {provider}
                </div>
                {groupedModels[provider].map((model) => {
                  const isSelected =
                    selectedModelId === model.id ||
                    model.id.split("@")[0] === selectedModelId;
                  return (
                    <button
                      key={model.id}
                      onClick={() => handleSelectModel(model.id)}
                      className={cn(
                        "w-full flex items-center justify-between px-2.5 py-1.5 rounded text-left transition-colors mx-0.5",
                        isSelected
                          ? "bg-primary/15"
                          : "hover:bg-muted/50",
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
                        <span className="text-[13px] font-medium text-foreground">
                          {model.name}
                        </span>
                      </div>
                      <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground/70">
                        {model.canReason && (
                          <span className="inline-flex px-1 py-px rounded-sm text-[9px] font-semibold uppercase tracking-tight bg-primary/15 text-primary">
                            reasoning
                          </span>
                        )}
                        {model.capabilities?.includes("fast") && (
                          <span className="inline-flex px-1 py-px rounded-sm text-[9px] font-semibold uppercase tracking-tight bg-sky-400/15 text-sky-400">
                            fast
                          </span>
                        )}
                      </div>
                    </button>
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
        <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70 mb-2">
          Overrides
        </div>

        {/* Thinking Level */}
        <div className="flex items-center justify-between mb-2">
          <span className="text-xs text-muted-foreground font-medium">
            Thinking
            {modelDefaultThinking && (
              <span className="text-[10px] text-muted-foreground/70 font-normal">
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
                {level.label}
              </option>
            ))}
          </select>
        </div>

        {/* Temperature */}
        <div className="flex items-center justify-between mb-2">
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
            <span className="text-[11px] text-muted-foreground min-w-7 text-right">
              {currentTemperature !== undefined
                ? currentTemperature.toFixed(1)
                : "1.0"}
            </span>
          </div>
        </div>

        {/* Compaction */}
        <div className="flex items-center justify-between">
          <span className="text-xs text-muted-foreground font-medium">
            Compaction
          </span>
          <input
            type="number"
            value={currentCompaction ?? ""}
            placeholder={
              selectedModel?.metadata?.defaultCompaction
                ? String(selectedModel.metadata.defaultCompaction)
                : "185000"
            }
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
      </div>
    </div>
  );
}