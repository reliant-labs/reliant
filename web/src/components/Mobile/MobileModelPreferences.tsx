/**
 * Trimmed mobile default-model picker.
 *
 * The desktop `ModelPreferences` is 465 lines because it exposes four knobs
 * per tag (model, thinking level, temperature, compaction threshold) behind
 * an expand/collapse row. Thinking level, temperature and compaction are
 * power-user tuning that assume a keyboard and a reason to be there — this
 * keeps only the one thing worth a thumb tap: which model each tier
 * resolves to. It reads and writes the SAME `model.tag_config.<tag>`
 * settings keys via the desktop module's exported helpers, so a choice made
 * here is the choice desktop's "Advanced model tuning" panel shows, and a
 * temperature/thinking override set on desktop survives a mobile model
 * change untouched (this only ever patches `model_id`).
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { ChevronDown, Loader2 } from "lucide-react";
import { useModels } from "../../store/globalDataStore";
import {
  loadTagModelConfigs,
  saveTagConfig,
  type TagModelConfig,
} from "../Settings/ModelPreferences";

const PREFERENCE_TAGS = [
  "powerful",
  "flagship",
  "moderate",
  "fast",
  "cheap",
] as const;
type PreferenceTag = (typeof PREFERENCE_TAGS)[number];

const TAG_LABELS: Record<PreferenceTag, string> = {
  powerful: "Powerful",
  flagship: "Flagship",
  moderate: "Moderate",
  fast: "Fast",
  cheap: "Cheap",
};

// "claude" provider uses the "anthropic" driver — models report driverId
// "anthropic", so matching only on provider name would miss them. Module
// scope since it's a constant, not derived state.
const PROVIDER_TO_DRIVER: Record<string, string> = { claude: "anthropic" };

interface MobileModelPreferencesProps {
  providers: Array<{ provider: string; hasApiKey: boolean }>;
}

export function MobileModelPreferences({
  providers,
}: MobileModelPreferencesProps) {
  const { models, loading: modelsLoading } = useModels();
  const [configs, setConfigs] = useState<Record<string, TagModelConfig>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState<Record<string, boolean>>({});

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
    [configuredProviders],
  );

  const allConfiguredModels = useMemo(
    () => models.filter(isConfiguredModel),
    [models, isConfiguredModel],
  );

  const handleModelChange = async (tag: PreferenceTag, modelId: string) => {
    const current = configs[tag] ?? {};
    const updated: TagModelConfig = { ...current, model_id: modelId || undefined };
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

  if (allConfiguredModels.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        Add a provider above to choose default models.
      </p>
    );
  }

  return (
    <div className="space-y-3">
      {PREFERENCE_TAGS.map((tag) => {
        const config = configs[tag] ?? {};
        const isSaving = saving[tag] ?? false;
        return (
          <div key={tag} className="flex items-center gap-3">
            <label className="w-20 shrink-0 text-sm font-medium text-foreground">
              {TAG_LABELS[tag]}
            </label>
            <div className="relative flex-1">
              <select
                value={config.model_id ?? ""}
                onChange={(e) => handleModelChange(tag, e.target.value)}
                disabled={isSaving}
                aria-label={`Default ${TAG_LABELS[tag]} model`}
                // min-h-[44px] on a native <select> — the tap target must
                // clear the floor even though this isn't a button/onClick
                // element the source guard scans for.
                className="min-h-[44px] w-full appearance-none rounded-md border border-input bg-background px-3 py-2 pr-8 text-sm disabled:opacity-50"
              >
                <option value="">Auto</option>
                {allConfiguredModels.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name} ({m.provider})
                  </option>
                ))}
              </select>
              <ChevronDown className="pointer-events-none absolute right-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            </div>
            {isSaving && (
              <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-muted-foreground" />
            )}
          </div>
        );
      })}
    </div>
  );
}
