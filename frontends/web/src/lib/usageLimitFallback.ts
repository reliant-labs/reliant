// Copyright (c) 2025 Reliant Labs

/**
 * What the waiting card can offer in place of waiting: the model this chat runs
 * on a different connected provider ("Use GPT-5.6 Sol · Codex now"), and which
 * of the limited provider's accounts is back first. Pure.
 */

import type { ProviderAccount } from "@/api/provider-account-grpc";
import { findPinnedModel, splitModelId } from "@/lib/modelId";

/** The slice of the model catalog this needs. */
export interface FallbackModel {
  /** "<model>@<driver>". */
  id: string;
  name: string;
  /** Provider display name ("Codex"). */
  provider: string;
  driverId?: string;
  tags?: string[];
}

export interface FallbackOffer {
  /** "<model>@<driver>": what RetryUsageLimitWait.use_model takes. */
  modelAtDriver: string;
  /** "GPT-5.6 Sol · Codex". */
  label: string;
}

export interface UsageLimitFallback {
  /** The driver the chat's own model runs on, i.e. the one that is limited. */
  limitedDriver?: string;
  offer?: FallbackOffer;
}

interface ModelPick {
  id?: string;
  tag?: string;
}

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return typeof v === "object" && v !== null && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : undefined;
}

function pickOf(v: unknown): ModelPick | undefined {
  if (typeof v === "string") return v ? { id: v } : undefined;
  const rec = asRecord(v);
  if (!rec) return undefined;
  if (typeof rec.id === "string" && rec.id) return { id: rec.id };
  const tag = Array.isArray(rec.tags) ? rec.tags.find((t) => typeof t === "string") : undefined;
  return tag ? { tag: tag as string } : undefined;
}

/** The chat's model param: the workflow's own, else the first group's. */
export function selectedModelPick(params: unknown): ModelPick | undefined {
  const root = asRecord(params);
  if (!root) return undefined;
  const own = pickOf(root.model);
  if (own) return own;
  for (const value of Object.values(root)) {
    const group = asRecord(value);
    const nested = group ? pickOf(group.model) : undefined;
    if (nested) return nested;
  }
  return undefined;
}

/** The model a pick runs: a pin by id, a tag through the user's tier table. */
function resolveModel(
  pick: ModelPick,
  models: readonly FallbackModel[],
  tiers: Record<string, { modelId: string }> | undefined,
): FallbackModel | undefined {
  if (pick.id) return findPinnedModel(pick.id, models);
  if (!pick.tag) return undefined;
  const tiered = tiers?.[pick.tag]?.modelId;
  return tiered
    ? findPinnedModel(tiered, models)
    : models.find((m) => m.tags?.includes(pick.tag!));
}

export function usageLimitFallbackOf(input: {
  params: unknown;
  /** The chat's stored "when limited, use <model>@<driver>", if any. */
  policyFallbackModel?: string;
  models: readonly FallbackModel[];
  tiers?: Record<string, { modelId: string }>;
}): UsageLimitFallback {
  const pick = selectedModelPick(input.params);
  const current = pick ? resolveModel(pick, input.models, input.tiers) : undefined;
  if (!current?.driverId) return {};

  // Local models run on a person's own machine: not a stand-in for a hosted one.
  const others = input.models.filter(
    (m) => m.driverId && m.driverId !== current.driverId && m.driverId !== "local",
  );
  const chosen =
    (input.policyFallbackModel && others.find((m) => m.id === input.policyFallbackModel)) ||
    others.find((m) => splitModelId(m.id).modelId === splitModelId(current.id).modelId) ||
    (() => {
      const tag = pick?.tag ?? current.tags?.[0];
      return tag ? others.find((m) => m.tags?.includes(tag)) : undefined;
    })();

  return {
    limitedDriver: current.driverId,
    offer: chosen
      ? { modelAtDriver: chosen.id, label: `${chosen.name} · ${chosen.provider}` }
      : undefined,
  };
}

/**
 * The provider's account that comes back first, named only when the provider
 * has more than one (with one, the reset is simply that account's).
 */
export function earliestResettingAccount(
  accounts: readonly ProviderAccount[],
  driver: string | undefined,
  now: number,
): { account: ProviderAccount; resetsAt: number } | undefined {
  if (!driver) return undefined;
  const ofProvider = accounts.filter((a) => a.provider === driver);
  if (ofProvider.length < 2) return undefined;
  let best: { account: ProviderAccount; resetsAt: number } | undefined;
  for (const account of ofProvider) {
    if (account.status !== "limited" || !account.limitedUntil) continue;
    const resetsAt = Date.parse(account.limitedUntil);
    if (Number.isNaN(resetsAt) || resetsAt <= now) continue;
    if (!best || resetsAt < best.resetsAt) best = { account, resetsAt };
  }
  return best;
}
