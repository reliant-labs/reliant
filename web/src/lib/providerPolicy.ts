// Copyright (c) 2025 Reliant Labs

import { ProviderPolicy_OnLimit } from "../gen/reliant/v1/chat_pb";

/** A model as the on-limit row needs it: the catalog's id ("<model>@<driver>") and display fields. */
export interface PolicyModel {
  id: string;
  name: string;
  provider: string;
  driverId?: string;
  tags?: string[];
  local?: unknown;
}

/** What a chat or a provider does when its provider is rate-limited. */
export type OnLimitChoice =
  | { kind: "inherit" }
  | { kind: "wait" }
  | { kind: "fallback"; model: string };

/** The Settings default, by driver id: the setting `provider.policy.defaults`. */
export type PolicyDefaults = Record<string, { on_limit?: string; fallback_model?: string }>;

export const POLICY_DEFAULTS_KEY = "provider.policy.defaults";

/** The driver of "<model>@<driver>", "" for an id that names none. */
export function driverOf(modelId: string): string {
  const at = modelId.lastIndexOf("@");
  return at > 0 ? modelId.slice(at + 1) : "";
}

/** The base model id of "<model>@<driver>". */
export function baseModelOf(modelId: string): string {
  const at = modelId.lastIndexOf("@");
  return at > 0 ? modelId.slice(0, at) : modelId;
}

/** The provider a model value (a pinned id, or a tier) runs on: the pinned driver, else the tier's resolution. */
export function primaryDriverOfSelection(
  value: unknown,
  tiers: Record<string, { modelId: string }> | undefined,
): string {
  if (!value || typeof value !== "object") return "";
  const v = value as { id?: string; tags?: string[]; providers?: string[] };
  if (v.id) return driverOf(v.id) || "";
  if (v.providers?.length) return v.providers[0];
  const tier = v.tags?.[0] ? tiers?.[v.tags[0]]?.modelId : undefined;
  return tier ? driverOf(tier) : "";
}

/** True for a model served by one machine's daemon: no provider limit applies. */
export function isLocalModel(model: Pick<PolicyModel, "driverId" | "local">): boolean {
  return model.driverId === "local" || !!model.local;
}

/**
 * The models one could fall back to when `primary` is limited: for each other
 * connected provider, the model that stands in for `selected` there (the same
 * model when the provider serves it, else the same tier), best first. The
 * server resolves the real substitute; this is the suggestion the picker shows.
 */
export function fallbackSuggestions(
  models: PolicyModel[],
  primary: string,
  selected: PolicyModel | null,
): PolicyModel[] {
  const out: PolicyModel[] = [];
  const seen = new Set<string>();
  const tags = selected?.tags ?? [];
  const byDriver = new Map<string, PolicyModel[]>();
  for (const m of models) {
    const driver = m.driverId ?? driverOf(m.id);
    if (!driver || driver === primary || isLocalModel(m)) continue;
    byDriver.set(driver, [...(byDriver.get(driver) ?? []), m]);
  }
  for (const [driver, list] of byDriver) {
    if (seen.has(driver)) continue;
    const same = selected ? list.find((m) => baseModelOf(m.id) === baseModelOf(selected.id)) : undefined;
    const sameTier = list.find((m) => tags.some((t) => m.tags?.includes(t)));
    const pick = same ?? sameTier ?? list[0];
    if (pick) {
      out.push(pick);
      seen.add(driver);
    }
  }
  return out;
}

/** The choice a ProviderPolicy message says. */
export function choiceFromPolicy(
  policy: { onLimit: ProviderPolicy_OnLimit; fallbackModel?: string } | undefined,
): OnLimitChoice {
  if (!policy || policy.onLimit === ProviderPolicy_OnLimit.UNSPECIFIED) return { kind: "inherit" };
  if (policy.onLimit === ProviderPolicy_OnLimit.FALLBACK && policy.fallbackModel) {
    return { kind: "fallback", model: policy.fallbackModel };
  }
  return { kind: "wait" };
}

/** The ProviderPolicy fields to send for a choice. */
export function policyFromChoice(choice: OnLimitChoice): { onLimit: ProviderPolicy_OnLimit; fallbackModel?: string } {
  switch (choice.kind) {
    case "wait":
      return { onLimit: ProviderPolicy_OnLimit.WAIT };
    case "fallback":
      return { onLimit: ProviderPolicy_OnLimit.FALLBACK, fallbackModel: choice.model };
    default:
      return { onLimit: ProviderPolicy_OnLimit.UNSPECIFIED };
  }
}

export function parsePolicyDefaults(value: string | undefined): PolicyDefaults {
  if (!value) return {};
  try {
    const parsed = JSON.parse(value);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as PolicyDefaults) : {};
  } catch {
    return {};
  }
}

/** The Settings default for a provider as a choice: no entry waits. */
export function choiceFromDefault(defaults: PolicyDefaults, driver: string): OnLimitChoice {
  const d = defaults[driver];
  if (d?.on_limit === "fallback" && d.fallback_model) return { kind: "fallback", model: d.fallback_model };
  return { kind: "wait" };
}

/** `defaults` with `driver` set to `choice`; "inherit" is not a default and clears the entry. */
export function withDefault(defaults: PolicyDefaults, driver: string, choice: OnLimitChoice): PolicyDefaults {
  const next = { ...defaults };
  if (choice.kind === "fallback") next[driver] = { on_limit: "fallback", fallback_model: choice.model };
  else if (choice.kind === "wait") next[driver] = { on_limit: "wait" };
  else delete next[driver];
  return next;
}
