// Copyright (c) 2025 Reliant Labs

/**
 * A refused visibility change in words (SharingService's reasons,
 * internal/handlers/sharing/share.go, and auth.ReasonPersonRequired): the
 * reason's sentence when the web knows it, else the server's own message.
 */

import { ConnectError } from "@connectrpc/connect";

import { errorReason } from "@/lib/errorReasons";

const REASON_WORDS: Record<string, string> = {
  no_org: "It belongs to no organization, so there is no one to share it with.",
  owner_only: "Only its owner can share it.",
  org_admin_required: "Only an admin or owner of the organization can make another member's item private.",
  person_required: "Sign in to the Reliant app to change who can read it; an access token or agent cannot.",
};

export function shareErrorMessage(err: unknown, fallback: string): string {
  const reason = errorReason(err);
  if (reason && REASON_WORDS[reason]) return REASON_WORDS[reason];
  if (err instanceof ConnectError) return err.rawMessage || fallback;
  if (err instanceof Error) return err.message || fallback;
  return fallback;
}
