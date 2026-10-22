import { useMemo, useState } from "react";
import { cn } from "../../../lib/utils";
import { useChatOnLimit, usePolicyDefaults } from "../../../hooks/provider-policy-queries";
import {
  baseModelOf,
  driverOf,
  fallbackSuggestions,
  isLocalModel,
  type OnLimitChoice,
  type PolicyModel,
} from "../../../lib/providerPolicy";

interface OnLimitRowProps {
  chatId: string;
  /** The provider the chat's model runs on ("claude", "codex", ...); "" when unknown. */
  primaryDriver: string;
  /** The model the chat runs, when it is a pinned model (a tier has none to name). */
  selectedModel: PolicyModel | null;
  /** Every model the user can run: the catalog the picker lists. */
  models: PolicyModel[];
  /** The model picker's driver display name ("Claude subscription", "Codex"). */
  providerName: (driver: string) => string;
}

const INHERIT = "inherit";
const WAIT = "wait";
const CHOOSE = "choose";
const fallbackValue = (model: string) => `fallback:${model}`;

/**
 * "If Claude is rate-limited: [ Wait ▾ ]" — what this chat does when its
 * provider's limit is hit. The choices are waiting for the reset, continuing on
 * a stand-in model on each OTHER connected provider, any model ("Choose…"), or
 * following the Settings default. Cross-provider fallback is never the default.
 *
 * Hidden for a local model (no provider limit). With no other provider
 * connected there is nothing to fall back to, so the row says so and offers
 * only waiting.
 */
export function OnLimitRow({ chatId, primaryDriver, selectedModel, models, providerName }: OnLimitRowProps) {
  const { choice, setChoice, saving, error } = useChatOnLimit(chatId);
  const { choiceFor } = usePolicyDefaults();
  const [choosing, setChoosing] = useState(false);

  const suggestions = useMemo(
    () => fallbackSuggestions(models, primaryDriver, selectedModel),
    [models, primaryDriver, selectedModel],
  );
  const others = useMemo(
    () => models.filter((m) => !isLocalModel(m) && (m.driverId ?? driverOf(m.id)) !== primaryDriver),
    [models, primaryDriver],
  );

  if (!primaryDriver || primaryDriver === "local" || (selectedModel && isLocalModel(selectedModel))) {
    return null;
  }

  const modelLabel = (id: string) => {
    const m = models.find((x) => x.id === id);
    const driver = driverOf(id);
    return `${m?.name ?? baseModelOf(id)} · ${providerName(driver)}`;
  };
  const describe = (c: OnLimitChoice) => (c.kind === "fallback" ? `Use ${modelLabel(c.model)}` : "Wait for the reset");

  const noOtherProvider = others.length === 0;
  const defaultChoice = choiceFor(primaryDriver);
  const value =
    choice.kind === "inherit" ? INHERIT : choice.kind === "wait" ? WAIT : fallbackValue(choice.model);
  const chosenIsListed = choice.kind !== "fallback" || suggestions.some((s) => s.id === choice.model);

  const onSelect = (next: string) => {
    if (next === CHOOSE) {
      setChoosing(true);
      return;
    }
    setChoosing(false);
    if (next === INHERIT) void setChoice({ kind: "inherit" }).catch(() => {});
    else if (next === WAIT) void setChoice({ kind: "wait" }).catch(() => {});
    else if (next.startsWith("fallback:")) void setChoice({ kind: "fallback", model: next.slice("fallback:".length) }).catch(() => {});
  };

  return (
    <div className="border-t border-border/50 px-3.5 py-2.5" data-testid="on-limit-row">
      <div className="flex items-center justify-between gap-2">
        <label htmlFor="on-limit-select" className="text-xs text-muted-foreground font-medium">
          If {providerName(primaryDriver)} is rate-limited:
        </label>
        <select
          id="on-limit-select"
          value={choosing ? CHOOSE : value}
          disabled={noOtherProvider ? false : saving}
          onChange={(e) => onSelect(e.target.value)}
          className={cn(
            "max-w-[190px] truncate px-2 py-1 bg-muted border border-border rounded text-foreground text-xs cursor-pointer outline-none hover:border-border/80",
          )}
        >
          <option value={WAIT}>Wait for the reset</option>
          {suggestions.map((m) => (
            <option key={m.id} value={fallbackValue(m.id)}>
              Use {m.name} · {providerName(m.driverId ?? driverOf(m.id))}
            </option>
          ))}
          {choice.kind === "fallback" && !chosenIsListed && (
            <option value={fallbackValue(choice.model)}>Use {modelLabel(choice.model)}</option>
          )}
          {!noOtherProvider && <option value={CHOOSE}>Choose…</option>}
          <option value={INHERIT}>Use Settings default ({describe(defaultChoice)})</option>
        </select>
      </div>

      {noOtherProvider && (
        <p className="mt-1 text-2xs text-muted-foreground/70" data-testid="on-limit-no-provider">
          Connect another provider to fall back to it.
        </p>
      )}

      {choosing && !noOtherProvider && (
        <select
          aria-label="Fall back to"
          defaultValue=""
          onChange={(e) => {
            if (!e.target.value) return;
            setChoosing(false);
            void setChoice({ kind: "fallback", model: e.target.value }).catch(() => {});
          }}
          className="mt-2 w-full px-2 py-1 bg-muted border border-border rounded text-foreground text-xs cursor-pointer outline-none"
        >
          <option value="" disabled>
            Pick a model on another provider…
          </option>
          {others.map((m) => (
            <option key={m.id} value={m.id}>
              {m.name} · {providerName(m.driverId ?? driverOf(m.id))}
            </option>
          ))}
        </select>
      )}

      {choice.kind === "fallback" && (
        <p className="mt-1 text-2xs text-muted-foreground/70">
          This chat keeps running on {modelLabel(choice.model)} until {providerName(primaryDriver)} resets, then returns.
        </p>
      )}
      {error && (
        <p role="alert" className="mt-1 text-2xs text-destructive-ink">
          Could not save this. Reload the chat and try again.
        </p>
      )}
    </div>
  );
}
