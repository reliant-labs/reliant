// Copyright (c) 2025 Reliant Labs

import { create } from "@bufbuild/protobuf";
import {
  ChatSelectionSchema,
  ProviderPolicy_OnLimit,
  ProviderPolicySchema,
  type ChatSelection,
} from "../gen/reliant/v1/chat_pb";
import type { JsonObject } from "@bufbuild/protobuf";
import { api } from "../api/client";
import { getChatSelection, setChatSelection } from "../hooks/chat-queries";
import { nestChangedParams, unsyncedParams } from "./paramUtils";

/**
 * A chat's selection as the update stream carries it (a CHAT_CONFIG_CHANGED
 * payload): the ChatSelection message's fields, snake-cased, JSON-encoded.
 */
export interface SelectionPayload {
  workflow_name?: string;
  params?: Record<string, unknown>;
  provider_policy?: { on_limit?: string; fallback_model?: string };
  version?: number | string;
  updated_at?: string;
}

const ON_LIMIT: Record<string, ProviderPolicy_OnLimit> = {
  wait: ProviderPolicy_OnLimit.WAIT,
  fallback: ProviderPolicy_OnLimit.FALLBACK,
};

export function selectionFromPayload(payload: SelectionPayload): ChatSelection {
  const policy = payload.provider_policy ?? {};
  return create(ChatSelectionSchema, {
    workflowName: payload.workflow_name ?? "",
    params: (payload.params ?? {}) as JsonObject,
    providerPolicy: create(ProviderPolicySchema, {
      onLimit: ON_LIMIT[policy.on_limit ?? ""] ?? ProviderPolicy_OnLimit.UNSPECIFIED,
      fallbackModel: policy.fallback_model || undefined,
    }),
    version: BigInt(payload.version ?? 0),
    updatedAt: payload.updated_at ?? "",
  });
}

/**
 * Whether `incoming` should replace `cached`. A selection only moves forward:
 * a stream event that arrives after a newer response (or a refetch) must not
 * roll the composer back.
 */
export function isNewerSelection(
  cached: ChatSelection | null | undefined,
  incoming: ChatSelection
): boolean {
  return !cached || incoming.version >= cached.version;
}

export interface PushedParams {
  /** The composer's flat keys now on the server (the changed ones, and every param of a changed group). */
  pushed: Record<string, unknown>;
  /** The selection as stored, after any unservable-model move. */
  selection?: ChatSelection;
}

/**
 * Push the composer's unsynced params to the chat's server-side selection.
 *
 * The server stores them whether or not a run is live, and signals a live run.
 * The edit names the selection version it was made against; a stale one is
 * refused with Code.Aborted and surfaces as the thrown ConnectError, which the
 * caller answers by refetching the selection. Resolves null when there is
 * nothing to push.
 *
 * `groups` are the schema's group names: the composer holds a group's params
 * flat ("<group>.<param>"), the server nested.
 */
export async function pushChatParams(
  chatId: string,
  params: Record<string, unknown>,
  synced: Record<string, unknown>,
  groups: ReadonlySet<string>
): Promise<PushedParams | null> {
  const changed = unsyncedParams(params, synced);
  if (Object.keys(changed).length === 0) return null;

  const patch = nestChangedParams(changed, params, groups);
  const cached = getChatSelection(chatId);
  const response = await api.chatsV2.updateWorkflowParams(
    chatId,
    patch,
    undefined,
    cached ? cached.version : undefined
  );

  // Every flat key the patch covered is now the server's.
  const pushed: Record<string, unknown> = { ...changed };
  for (const [key, value] of Object.entries(params)) {
    const group = key.includes(".") ? key.slice(0, key.indexOf(".")) : "";
    if (group && groups.has(group) && group in patch) pushed[key] = value;
  }
  if (response.selection) {
    setChatSelection(chatId, response.selection);
  }
  return { pushed, selection: response.selection };
}
