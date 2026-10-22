import { useCallback, useEffect, useMemo, useState } from "react";
import { Loader2 } from "lucide-react";
import { useModels } from "../../store/globalDataStore";
import {
  readSetting,
  upsertStringSetting,
  deleteSettingIfExists,
} from "../../lib/settingsPersistence";
import { cn } from "../../lib/utils";

// ---------------------------------------------------------------------------
// Storage
// ---------------------------------------------------------------------------
//
// This is NOT Model Preferences. That panel picks the model the CONVERSATION
// runs on, and its rows are quality tiers (flagship / moderate / fast / cheap).
// Image generation is a TOOL: the agent never picks its model, the tool
// resolves one independently, and a tier row would be answering a different
// question. So tool preferences get their own key space and their own surface.
//
// The stored shape is a tool's bound parameters, exactly as the server reads
// them — a parameter name to `{literal}` or `{expr}`:
//
//   tool.bindings.generate_image = {"model":{"literal":{"tags":["image-gen"],
//                                                       "providers":["codex"]}}}
//
// Unlike `model.tag_config.*`, which is written here and read ONLY here, this
// key is read SERVER-SIDE (internal/toolbindings.LoadGlobal, called from the
// call_llm activity). That is what makes it apply to every consumer of the
// tool — a workflow run, a spawned sub-agent, a scheduled job — rather than
// only to whatever the chat composer happens to send.

/** Settings key holding one tool's globally bound parameters. */
export const toolBindingsKey = (toolName: string) => `tool.bindings.${toolName}`;

/** One bound parameter: a fixed value, or an expression resolved per call. */
export interface ToolBinding {
  literal?: unknown;
  expr?: string;
}

/** A tool's bound parameters, keyed by parameter name. */
export type ToolBindings = Record<string, ToolBinding>;

/** A model selector, the shape `generate_image`'s `model` parameter takes. */
export interface ModelSelectorBinding {
  id?: string;
  tags?: string[];
  providers?: string[];
}

const GENERATE_IMAGE_TOOL = "generate_image";
const IMAGE_GEN_TAG = "image-gen";

/**
 * Load a tool's global bindings. Callable outside React.
 *
 * readSetting serves from the cache ListSettings already populated, so this
 * costs no extra round trip.
 */
export async function loadToolBindings(toolName: string): Promise<ToolBindings> {
  try {
    const read = await readSetting(toolBindingsKey(toolName));
    if (read.status !== "found") return {};
    const parsed: unknown = JSON.parse(read.value);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return parsed as ToolBindings;
  } catch {
    // A malformed row means "no preference recorded", never a broken panel.
    // The server takes the same posture: one bad row degrades that tool to its
    // defaults rather than failing anything.
    return {};
  }
}

/**
 * Persist a tool's global bindings, or clear them entirely.
 *
 * Deleting rather than writing `{}` keeps the settings table honest about what
 * a user has actually expressed an opinion on.
 */
export async function saveToolBindings(
  toolName: string,
  bindings: ToolBindings,
): Promise<void> {
  const key = toolBindingsKey(toolName);
  if (Object.keys(bindings).length === 0) {
    await deleteSettingIfExists(key);
    return;
  }
  await upsertStringSetting(key, JSON.stringify(bindings));
}

/**
 * The image model selector currently bound, or undefined for "automatic".
 *
 * Exported so a caller can read the preference without rendering the panel.
 */
export function imageModelSelector(
  bindings: ToolBindings,
): ModelSelectorBinding | undefined {
  const literal = bindings.model?.literal;
  if (!literal || typeof literal !== "object" || Array.isArray(literal)) {
    return undefined;
  }
  return literal as ModelSelectorBinding;
}

// ---------------------------------------------------------------------------
// Choices
// ---------------------------------------------------------------------------

/**
 * A provider choice for image generation, and which pocket it spends from.
 *
 * `spends` is not decoration. BYO subscriptions and managed credits are
 * genuinely different money: a Codex or OpenAI request goes straight to the
 * provider on the user's own credential and is NOT metered by the
 * control-plane proxy, while `reliant` routes through that proxy precisely so
 * the spend can be counted against managed credits. A user choosing a provider
 * is choosing a bill, and the UI has to say so.
 */
interface ProviderChoice {
  /** The driver id, as it appears in a ModelSelector's `providers`. */
  id: string;
  label: string;
  spends: string;
}

const PROVIDER_CHOICES: ProviderChoice[] = [
  {
    id: "reliant",
    label: "Reliant credits",
    spends: "Metered — draws down your Reliant AI credit balance.",
  },
  {
    id: "openai",
    label: "OpenAI (your API key)",
    spends: "Billed by OpenAI to your own key. Not metered by Reliant.",
  },
  {
    id: "codex",
    label: "ChatGPT / Codex (your subscription)",
    spends:
      "Covered by your own ChatGPT subscription. Not metered by Reliant, and no credits are used.",
  },
];

const AUTOMATIC = "__automatic__";

/**
 * Build the selector for one choice.
 *
 * The tag is always carried, and a concrete model id never is. Tags re-resolve
 * against the registry on every call, so a retired image model drops out on its
 * own; pinning an id keeps requesting it until someone notices, which is how
 * retired models shipped twice in one day. Choosing a provider therefore
 * narrows WHERE the request goes without freezing WHICH model serves it.
 */
function selectorForProvider(providerID: string): ModelSelectorBinding {
  return { tags: [IMAGE_GEN_TAG], providers: [providerID] };
}

// ---------------------------------------------------------------------------
// Panel
// ---------------------------------------------------------------------------

interface ToolPreferencesProps {
  providers: Array<{
    provider: string;
    configured: boolean;
  }>;
}

/**
 * Global preferences for tools that resolve something on the user's behalf.
 *
 * Today that is one question, asked properly: when several providers can
 * generate an image, which one does `generate_image` use — and whose money
 * does that spend?
 */
export function ToolPreferences({ providers }: ToolPreferencesProps) {
  const [bindings, setBindings] = useState<ToolBindings>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const { models } = useModels();

  useEffect(() => {
    let cancelled = false;
    void loadToolBindings(GENERATE_IMAGE_TOOL).then((loaded) => {
      if (!cancelled) {
        setBindings(loaded);
        setLoading(false);
      }
    });
    return () => {
      cancelled = true;
    };
  }, []);

  /**
   * Which providers can actually generate an image for this user: they have a
   * credential for it, AND the registry has an image model behind it.
   *
   * Derived from the registry rather than hardcoded, for the same reason
   * providerOutputModalities is derived server-side — a static list drifts
   * silently the moment a provider ships or retires an image endpoint, and the
   * UI keeps offering a choice that fails at call time.
   */
  const availableProviders = useMemo(() => {
    const configured = new Set(
      providers.filter((p) => p.configured).map((p) => p.provider),
    );
    const imageCapable = new Set(
      models
        .filter((m) => m.tags?.includes(IMAGE_GEN_TAG))
        .map((m) => m.driverId)
        .filter((id): id is string => Boolean(id)),
    );
    return PROVIDER_CHOICES.filter(
      (choice) => configured.has(choice.id) && imageCapable.has(choice.id),
    );
  }, [providers, models]);

  const selected = useMemo(() => {
    const provider = imageModelSelector(bindings)?.providers?.[0];
    return provider ?? AUTOMATIC;
  }, [bindings]);

  const choose = useCallback(async (value: string) => {
    setSaving(true);
    const next: ToolBindings =
      value === AUTOMATIC
        ? {}
        : { model: { literal: selectorForProvider(value) } };
    setBindings(next);
    try {
      await saveToolBindings(GENERATE_IMAGE_TOOL, next);
    } finally {
      setSaving(false);
    }
  }, []);

  if (loading) {
    return (
      <div className="flex items-center gap-2 p-4 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Loading tool preferences…
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h3 className="text-sm font-semibold text-foreground">
          Image generation
        </h3>
        <p className="mt-1 text-xs text-muted-foreground">
          When more than one provider can generate an image, this decides which
          one the <code className="font-mono">generate_image</code> tool uses —
          and whose bill it lands on. The agent never picks this; it applies to
          every chat, workflow and sub-agent.
        </p>
      </div>

      {availableProviders.length === 0 ? (
        <p className="rounded-lg border border-border/50 bg-muted/30 p-3 text-xs text-muted-foreground">
          No configured provider can generate images yet. Add an OpenAI key,
          connect ChatGPT, or enable Reliant credits, and this choice will
          appear.
        </p>
      ) : (
        <fieldset
          className="space-y-2"
          disabled={saving}
          aria-label="Image generation provider"
        >
          <ProviderOption
            id={AUTOMATIC}
            label="Automatic"
            spends="Prefers a provider you pay for directly, falling back to Reliant credits."
            checked={selected === AUTOMATIC}
            onSelect={choose}
          />
          {availableProviders.map((choice) => (
            <ProviderOption
              key={choice.id}
              id={choice.id}
              label={choice.label}
              spends={choice.spends}
              checked={selected === choice.id}
              onSelect={choose}
            />
          ))}
        </fieldset>
      )}
    </div>
  );
}

interface ProviderOptionProps {
  id: string;
  label: string;
  spends: string;
  checked: boolean;
  onSelect: (id: string) => void;
}

function ProviderOption({
  id,
  label,
  spends,
  checked,
  onSelect,
}: ProviderOptionProps) {
  return (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-3 rounded-lg border p-3 transition-colors",
        checked
          ? "border-primary bg-primary/5"
          : "border-border/50 hover:border-border",
      )}
    >
      <input
        type="radio"
        name="image-generation-provider"
        value={id}
        checked={checked}
        onChange={() => onSelect(id)}
        className="mt-0.5"
      />
      <span className="min-w-0">
        <span className="block text-sm font-medium text-foreground">
          {label}
        </span>
        <span className="mt-0.5 block text-xs text-muted-foreground">
          {spends}
        </span>
      </span>
    </label>
  );
}
