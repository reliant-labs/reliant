import { useMemo } from "react";
import { cn } from "../../../lib/utils";
import { driverDisplayName } from "../../../hooks/catalog-models";
import { useModels } from "../../../store/globalDataStore";
import { usePolicyDefaults } from "../../../hooks/provider-policy-queries";
import { baseModelOf, driverOf, fallbackSuggestions, type PolicyModel } from "../../../lib/providerPolicy";

interface OnLimitDefaultProps {
  /** The provider's driver id ("claude", "codex"). */
  provider: string;
  displayName: string;
  variant: "desktop" | "mobile";
}

const WAIT = "wait";

/**
 * "When all are limited: Wait | Use <model> · <provider>" — the default for
 * every chat on this provider that has not chosen for itself (the composer's
 * row). It is stored as the setting `provider.policy.defaults`. Waiting is the
 * default; falling back to another provider is always opt-in.
 *
 * It sits on the card's header line beside Rotation, because the two are one
 * decision: which account to use, and what to do once none is left. With one
 * provider there is nothing to fall back to, so the control is replaced by a
 * line that says how to get it — a feature you cannot see is one you never
 * learn exists.
 */
export function OnLimitDefault({ provider, displayName, variant }: OnLimitDefaultProps) {
  const { models } = useModels();
  const { choiceFor, setChoice } = usePolicyDefaults();

  const suggestions = useMemo(
    () => fallbackSuggestions(models as PolicyModel[], provider, null),
    [models, provider],
  );
  if (suggestions.length === 0) {
    return (
      <span className="text-xs text-muted-foreground" data-testid="on-limit-unavailable">
        Connect another provider to fall back when {displayName} is limited
      </span>
    );
  }

  const choice = choiceFor(provider);
  const value = choice.kind === "fallback" ? choice.model : WAIT;
  const label = (id: string) => {
    const m = models.find((x) => x.id === id);
    return `${m?.name ?? baseModelOf(id)} · ${driverDisplayName(driverOf(id))}`;
  };
  const listed = choice.kind !== "fallback" || suggestions.some((s) => s.id === choice.model);
  const explanation =
    choice.kind === "fallback"
      ? "Chats continue on that model until the reset, then return. A chat can override this in its model settings."
      : "Chats wait for the reset. A chat can choose to fall back in its model settings.";

  return (
    <label
      className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground"
      data-testid="on-limit-default"
      title={explanation}
    >
      <span>When all are limited</span>
      <select
        aria-label={`When ${displayName} is rate-limited`}
        value={value}
        onChange={(e) =>
          void setChoice(
            provider,
            e.target.value === WAIT ? { kind: "wait" } : { kind: "fallback", model: e.target.value },
          ).catch(() => {})
        }
        className={cn(
          "rounded-md border border-border bg-background px-2 text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-ring/40",
          variant === "mobile" ? "min-h-[44px] text-sm" : "py-0.5",
        )}
      >
        <option value={WAIT}>Wait for the reset</option>
        {suggestions.map((m) => (
          <option key={m.id} value={m.id}>
            Use {label(m.id)}
          </option>
        ))}
        {!listed && choice.kind === "fallback" && <option value={choice.model}>Use {label(choice.model)}</option>}
      </select>
    </label>
  );
}
