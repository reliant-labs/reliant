import { useState, useEffect, useCallback, useMemo } from "react";
import { Loader2, ChevronDown, ChevronRight } from "lucide-react";
import { useModels } from "../../store/globalDataStore";
import {
  readSetting,
  upsertStringSetting,
  deleteSettingIfExists,
} from "../../lib/settingsPersistence";
import { cn } from "../../lib/utils";

// ---------------------------------------------------------------------------
// Types & constants
// ---------------------------------------------------------------------------

const PREFERENCE_TAGS = ["flagship", "moderate", "fast", "cheap"] as const;
type PreferenceTag = (typeof PREFERENCE_TAGS)[number];

const TAG_DESCRIPTIONS: Record<PreferenceTag, string> = {
  flagship: "Best overall — complex tasks, deep reasoning",
  moderate: "Balanced — implementation, review",
  fast: "Speed optimized — research, simple tasks",
  cheap: "Lowest cost — bulk, internal",
};

/** Settings key for a tag's full model config JSON. */
const tagSettingsKey = (tag: string) => `model.tag_config.${tag}`;

/** Per-tag model configuration stored in settings. */
export interface TagModelConfig {
  model_id?: string;           // explicit model override (empty = auto-resolve)
  thinking_level?: string;     // "" = auto / model default
  temperature?: number;        // undefined = auto / model default
  compaction_threshold?: number;
}

const THINKING_LEVELS = [
  { value: "", label: "Auto" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "Extra High" },
  { value: "max", label: "Max" },
  { value: "ultra", label: "Ultra" },
] as const;

// ---------------------------------------------------------------------------
// Public API — loadable outside React
// ---------------------------------------------------------------------------

/** Load all tag model configs from settings. Callable outside React. */
export async function loadTagModelConfigs(): Promise<
  Record<string, TagModelConfig>
> {
  const result: Record<string, TagModelConfig> = {};
  try {
    // readSetting serves from the cache ListSettings already populated, so
    // this reads all N tags without N GetSetting round trips.
    const settled = await Promise.allSettled(
      PREFERENCE_TAGS.map(async (tag) => {
        const read = await readSetting(tagSettingsKey(tag));
        return { tag, value: read.status === "found" ? read.value : undefined };
      })
    );
    for (const r of settled) {
      if (r.status === "fulfilled" && r.value.value) {
        try {
          result[r.value.tag] = JSON.parse(r.value.value) as TagModelConfig;
        } catch {
          // bad JSON, skip
        }
      }
    }
  } catch {
    // settings may not exist
  }
  return result;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Save a tag's model config to settings. Callable outside React (e.g. the mobile default-model picker). */
export async function saveTagConfig(tag: string, config: TagModelConfig) {
  const key = tagSettingsKey(tag);
  const isEmpty =
    !config.model_id &&
    !config.thinking_level &&
    config.temperature === undefined &&
    config.compaction_threshold === undefined;

  if (isEmpty) {
    await deleteSettingIfExists(key);
  } else {
    // Coalesced + auth-gated like every other setting write. Saving the
    // preferences panel touches several tags at once.
    await upsertStringSetting(key, JSON.stringify(config));
  }
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

interface ModelPreferencesProps {
  providers: Array<{
    provider: string;
    hasApiKey: boolean;
  }>;
}

export function ModelPreferences({ providers }: ModelPreferencesProps) {
  const { models, loading: modelsLoading } = useModels();
  const [configs, setConfigs] = useState<Record<string, TagModelConfig>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState<Record<string, boolean>>({});
  const [expandedTag, setExpandedTag] = useState<string | null>(null);

  // Build a set of configured provider/driver names for matching models.
  // Settings providers use names like "claude", "codex", "anthropic", "openai".
  // Models have provider ("Anthropic") and driverId ("anthropic").
  // "claude" provider uses the "anthropic" driver, so we map it.
  const PROVIDER_TO_DRIVER: Record<string, string> = { claude: "anthropic" };
  const configuredProviders = useMemo(() => {
    const set = new Set<string>();
    for (const p of providers) {
      if (!p.hasApiKey) continue;
      const key = p.provider.toLowerCase();
      set.add(key);
      if (PROVIDER_TO_DRIVER[key]) set.add(PROVIDER_TO_DRIVER[key]);
    }
    return set;
  }, [providers]);

  // Load configs
  useEffect(() => {
    loadTagModelConfigs()
      .then(setConfigs)
      .catch((e) => console.error("Failed to load tag configs:", e))
      .finally(() => setLoading(false));
  }, []);

  const isConfiguredModel = useCallback(
    (m: { provider: string; driverId?: string }) =>
      configuredProviders.has(m.provider.toLowerCase()) ||
      (m.driverId ? configuredProviders.has(m.driverId.toLowerCase()) : false),
    [configuredProviders]
  );

  // Models for a given tag (only from configured providers)
  const getModelsForTag = useCallback(
    (tag: string) =>
      models.filter((m) => m.tags?.includes(tag) && isConfiguredModel(m)),
    [models, isConfiguredModel]
  );

  // All models from configured providers (for the explicit picker)
  const allConfiguredModels = useMemo(
    () => models.filter(isConfiguredModel),
    [models, isConfiguredModel]
  );

  const groupByProvider = useCallback(
    (list: typeof models) => {
      const groups: Record<string, typeof models> = {};
      for (const m of list) {
        const p = m.provider;
        if (!groups[p]) groups[p] = [];
        groups[p].push(m);
      }
      return Object.entries(groups).sort(([a], [b]) => a.localeCompare(b));
    },
    []
  );

  // Resolve tag to the default model (first model with that tag)
  const resolveTag = useCallback(
    (tag: string) => {
      const tagModels = getModelsForTag(tag);
      return tagModels[0] ?? null;
    },
    [getModelsForTag]
  );

  const handleFieldChange = async (
    tag: PreferenceTag,
    field: keyof TagModelConfig,
    value: string | number | undefined
  ) => {
    const current = configs[tag] ?? {};
    const updated = { ...current, [field]: value };

    // Clean undefined/empty values
    if (value === undefined || value === "" || value === null) {
      delete (updated as Record<string, unknown>)[field];
    }

    setConfigs((prev) => ({ ...prev, [tag]: updated }));
    setSaving((prev) => ({ ...prev, [tag]: true }));
    try {
      await saveTagConfig(tag, updated);
    } catch (e) {
      console.error(`Failed to save ${tag} config:`, e);
    } finally {
      setSaving((prev) => ({ ...prev, [tag]: false }));
    }
  };

  if (modelsLoading || loading) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Loading models...
      </div>
    );
  }

  return (
    <div>
      <p className="text-sm text-muted-foreground mb-4">
        These apply to every new chat — override per-chat in the model settings
        popover.
      </p>

      <div className="space-y-1">
        {PREFERENCE_TAGS.map((tag) => {
          const config = configs[tag] ?? {};
          const tagModels = getModelsForTag(tag);
          const grouped = groupByProvider(tagModels);
          const isExpanded = expandedTag === tag;
          const isSaving = saving[tag] ?? false;

          // Display info
          const resolvedModel = config.model_id
            ? models.find(
                (m) =>
                  m.id === config.model_id ||
                  m.id.split("@")[0] === config.model_id
              )
            : resolveTag(tag);
          const displayName = resolvedModel?.name ?? "auto";
          const hasOverrides =
            !!config.model_id ||
            !!config.thinking_level ||
            config.temperature !== undefined ||
            config.compaction_threshold !== undefined;

          return (
            <div
              key={tag}
              className={cn(
                "rounded-lg border transition-colors",
                isExpanded
                  ? "border-border bg-muted/30"
                  : "border-transparent hover:bg-muted/20"
              )}
            >
              {/* Collapsed row */}
              <button
                type="button"
                onClick={() => setExpandedTag(isExpanded ? null : tag)}
                className="w-full flex items-center gap-3 px-3 py-2.5 text-left"
              >
                <ChevronRight
                  className={cn(
                    "w-3.5 h-3.5 text-muted-foreground shrink-0 transition-transform",
                    isExpanded && "rotate-90"
                  )}
                />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-semibold capitalize">
                      {tag}
                    </span>
                    {hasOverrides && (
                      <span className="text-2xs px-1.5 py-0.5 rounded-full bg-primary/10 text-primary font-medium">
                        customized
                      </span>
                    )}
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {TAG_DESCRIPTIONS[tag]}
                  </span>
                </div>
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground shrink-0">
                  {isSaving && (
                    <Loader2 className="w-3 h-3 animate-spin" />
                  )}
                  <span>{displayName}</span>
                </div>
              </button>

              {/* Expanded config */}
              {isExpanded && (
                <div className="px-3 pb-3 space-y-3 border-t border-border/50 pt-3 ml-6">
                  {/* Model */}
                  <div className="flex items-center gap-3">
                    <label className="text-xs font-medium text-muted-foreground w-24 shrink-0">
                      Model
                    </label>
                    <div className="relative flex-1">
                      <select
                        value={config.model_id ?? ""}
                        onChange={(e) =>
                          handleFieldChange(
                            tag,
                            "model_id",
                            e.target.value || undefined
                          )
                        }
                        className="w-full px-2.5 py-1.5 pr-8 border border-input bg-background rounded-md appearance-none cursor-pointer text-xs disabled:opacity-50"
                      >
                        <option value="">Auto (first available)</option>
                        {grouped.map(([provider, providerModels]) => (
                          <optgroup
                            key={provider}
                            label={provider}
                          >
                            {providerModels.map((m) => (
                              <option key={m.id} value={m.id}>
                                {m.name}
                              </option>
                            ))}
                          </optgroup>
                        ))}
                        {/* Also show all models in case user wants one not tagged for this tier */}
                        {allConfiguredModels.length > tagModels.length && (
                          <optgroup label="── All models ──">
                            {allConfiguredModels
                              .filter((m) => !tagModels.includes(m))
                              .map((m) => (
                                <option key={m.id} value={m.id}>
                                  {m.name} ({m.provider})
                                </option>
                              ))}
                          </optgroup>
                        )}
                      </select>
                      <ChevronDown className="absolute right-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground pointer-events-none" />
                    </div>
                  </div>

                  {/* Thinking Level */}
                  <div className="flex items-center gap-3">
                    <label className="text-xs font-medium text-muted-foreground w-24 shrink-0">
                      Thinking
                    </label>
                    <div className="relative flex-1">
                      <select
                        value={config.thinking_level ?? ""}
                        onChange={(e) =>
                          handleFieldChange(
                            tag,
                            "thinking_level",
                            e.target.value || undefined
                          )
                        }
                        className="w-full px-2.5 py-1.5 pr-8 border border-input bg-background rounded-md appearance-none cursor-pointer text-xs"
                      >
                        {THINKING_LEVELS.map((l) => (
                          <option key={l.value} value={l.value}>
                            {l.label}
                          </option>
                        ))}
                      </select>
                      <ChevronDown className="absolute right-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground pointer-events-none" />
                    </div>
                  </div>

                  {/* Temperature */}
                  <div className="flex items-center gap-3">
                    <label className="text-xs font-medium text-muted-foreground w-24 shrink-0">
                      Temperature
                    </label>
                    <div className="flex items-center gap-2 flex-1">
                      <input
                        type="range"
                        min="0"
                        max="100"
                        value={
                          config.temperature !== undefined
                            ? Math.round(config.temperature * 100)
                            : 100
                        }
                        onChange={(e) =>
                          handleFieldChange(
                            tag,
                            "temperature",
                            Number(e.target.value) / 100
                          )
                        }
                        className="flex-1 h-1.5 appearance-none bg-muted rounded cursor-pointer accent-primary"
                      />
                      <span className="text-xs text-muted-foreground min-w-8 text-right tabular-nums">
                        {config.temperature !== undefined
                          ? config.temperature.toFixed(1)
                          : "auto"}
                      </span>
                      {config.temperature !== undefined && (
                        <button
                          onClick={() =>
                            handleFieldChange(tag, "temperature", undefined)
                          }
                          className="text-2xs text-muted-foreground hover:text-foreground px-1"
                        >
                          ×
                        </button>
                      )}
                    </div>
                  </div>

                  {/* Compaction */}
                  <div className="flex items-center gap-3">
                    <label className="text-xs font-medium text-muted-foreground w-24 shrink-0">
                      Compaction
                    </label>
                    <div className="flex items-center gap-2 flex-1">
                      <input
                        type="number"
                        value={config.compaction_threshold ?? ""}
                        placeholder="auto"
                        onChange={(e) =>
                          handleFieldChange(
                            tag,
                            "compaction_threshold",
                            e.target.value
                              ? Number(e.target.value)
                              : undefined
                          )
                        }
                        step={5000}
                        className="w-24 px-2.5 py-1.5 border border-input bg-background rounded-md text-xs text-right"
                      />
                      <span className="text-2xs text-muted-foreground">
                        tokens
                      </span>
                      {config.compaction_threshold !== undefined && (
                        <button
                          onClick={() =>
                            handleFieldChange(
                              tag,
                              "compaction_threshold",
                              undefined
                            )
                          }
                          className="text-2xs text-muted-foreground hover:text-foreground px-1"
                        >
                          ×
                        </button>
                      )}
                    </div>
                  </div>
                </div>
              )}
            </div>
          );
        })}
      </div>

      {models.length === 0 && (
        <div className="text-xs text-muted-foreground mt-3 p-3 rounded-md elevation-1">
          No models available. Add an API key above to see available models.
        </div>
      )}

      <div className="text-xs text-muted-foreground mt-3 p-3 rounded-md elevation-1">
        <strong>Note:</strong> Only models from configured providers are shown.
        Add more providers above to see additional models.
      </div>
    </div>
  );
}