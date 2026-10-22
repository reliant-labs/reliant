// Copyright (c) 2025 Reliant Labs

import { repairLegacyWorkflowParams } from "./paramUtils";

/**
 * A chat's composer params used to live only in this browser profile's
 * localStorage (`reliant-chat-params`); they now live on the server, which is
 * what every device, restart and branch reads. What an older build left behind
 * is read once per chat, to seed the server's copy of a chat that has none, and
 * is deleted as soon as the server has it. Nothing writes this key any more, and
 * nothing reads it for a chat the server already holds a selection for.
 */
const STORAGE_KEY = "reliant-chat-params";

interface LegacyBlob {
  version?: number;
  state?: {
    chatParams?: Record<string, unknown>;
    chatPresets?: Record<string, unknown>;
  };
}

function readBlob(): LegacyBlob | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as LegacyBlob) : null;
  } catch {
    return null;
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * The params an older build stored for the chat, repaired the way a send is
 * (a top-level thinking_level, "default." preset keys), or undefined when there
 * are none. Group params keep their flat "<group>.<param>" keys: that is how the
 * composer holds them.
 *
 * Reading does not delete: call clearLegacyChatParams once the server has the
 * values, so a failed write does not lose them.
 */
export function peekLegacyChatParams(chatId: string): Record<string, unknown> | undefined {
  const blob = readBlob();
  // Before v5 the stored params were cleared by migration, not carried forward.
  if (!blob || (blob.version ?? 0) < 5) return undefined;
  const stored = blob.state?.chatParams?.[chatId];
  if (!isPlainObject(stored)) return undefined;

  const params: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(stored)) {
    if (key.startsWith("__")) continue; // metadata that leaked into the params (v5)
    params[key] = value;
  }
  const repaired = repairLegacyWorkflowParams(params);
  return Object.keys(repaired).length > 0 ? repaired : undefined;
}

/** Forget the chat's legacy params, and the whole key once none are left. */
export function clearLegacyChatParams(chatId: string): void {
  const blob = readBlob();
  if (!blob?.state) return;
  const { chatParams, chatPresets } = blob.state;
  const hadParams = !!chatParams && chatId in chatParams;
  const hadPresets = !!chatPresets && chatId in chatPresets;
  if (!hadParams && !hadPresets) return;

  if (chatParams) delete chatParams[chatId];
  if (chatPresets) delete chatPresets[chatId];
  try {
    const left = Object.keys(chatParams ?? {}).length + Object.keys(chatPresets ?? {}).length;
    if (left === 0) {
      localStorage.removeItem(STORAGE_KEY);
    } else {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(blob));
    }
  } catch {
    // Storage full or unavailable: the leftover is read again next time and is harmless.
  }
}
