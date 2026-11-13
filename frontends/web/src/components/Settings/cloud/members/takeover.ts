// Copyright (c) 2025 Reliant Labs

/**
 * Shared words and helpers of the automation takeover (control-plane
 * docs/design/org-sharing.md §8): an org admin takes back the automations of
 * a member who left, adopting them or offering them to a member who accepts.
 */

import { ConnectError } from "@connectrpc/connect";

import type { Trigger } from "@/gen/reliant/v1/trigger_pb";

/** The native select the members page uses (InviteDialog). */
export const SELECT_CLASS =
  "block w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring";

const REASON_WORDS: Record<string, string> = {
  org_admin_required: "Only an admin or owner of this organization can do this.",
  automation_not_orphaned: "Its creator is a member of the organization again, so it is theirs.",
  offeree_not_member: "That person is no longer a member of this organization.",
  person_required: "Sign in to the Reliant app to do this; an access token or agent cannot.",
};

/** A refusal in words: the reason's sentence, else the server's own message. */
export function takeoverErrorMessage(err: unknown, fallback: string): string {
  if (err instanceof ConnectError) {
    const reason = err.metadata.get("x-forge-error-reason") ?? "";
    // An offer that is no longer live says why in the server's words
    // (expired, withdrawn, a party left), which are the useful ones.
    if (reason && REASON_WORDS[reason]) return REASON_WORDS[reason];
    return err.rawMessage || fallback;
  }
  return fallback;
}

/** What makes the automation fire, in a few words. */
export function sourceLabel(trigger: Trigger | undefined): string {
  switch (trigger?.source.case) {
    case "schedule":
      return "On a schedule";
    case "webhook":
      return "On a webhook";
    case "integration":
      return `On ${trigger.source.value.integration || "an integration"} events`;
    case "workflowEvent":
      return "When a run finishes";
    default:
      return "Automation";
  }
}

/** Whether the webhook verifies signatures, so adopting needs a new secret. */
export function needsSigningSecret(trigger: Trigger | undefined): boolean {
  return trigger?.source.case === "webhook" && !!trigger.source.value.hmac && !trigger.webhookManaged;
}
