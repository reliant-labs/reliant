// Copyright (c) 2025 Reliant Labs

/**
 * Names for the ids in a trigger's "Only from" list. The list stores what
 * trigger.sender.id carries — on GitHub a numeric user id, because a login can
 * be renamed and reclaimed — and people read names, so each id is shown by
 * the first of:
 *
 *  1. "Me" (the caller's own connection or account, which knows its login),
 *  2. a name learned this session (a login resolved when it was added),
 *  3. the trigger's own recorded firings (trigger.sender.display_name),
 *  4. on GitHub, the login GitHub reports for the id now, looked up as the
 *     caller (ResolveTriggerSenders) — only for ids 1–3 cannot name.
 *
 * An id none of them can name is shown as itself.
 */

import { useCallback, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { triggerGrpc } from "../api/trigger-grpc";
import { isGitHubUserId } from "../lib/onlyFromFilter";
import type { MySender } from "./useMySenderId";
import { triggerKeys, useTriggerEvents } from "./trigger-queries";

/** trigger.sender.kind for each integration "Only from" covers. */
const SENDER_KIND: Record<string, string> = { slack: "slack", github: "github", gmail: "email" };

export interface SenderNames {
  /** The name to show for id, when one is known. */
  nameOf: (id: string) => string | undefined;
  /** Whether names are still being looked up. */
  looking: boolean;
  /** Remember a name learned elsewhere: a login resolved when it was added. */
  remember: (id: string, name: string) => void;
}

export function useSenderNames(
  integration: string,
  ids: readonly string[],
  options: { triggerId?: string; me?: MySender } = {},
): SenderNames {
  const { triggerId, me } = options;
  const [learned, setLearned] = useState<Record<string, string>>({});
  const events = useTriggerEvents(triggerId, { refetchInterval: false });

  const known = useMemo(() => {
    const names = new Map<string, string>();
    const kind = SENDER_KIND[integration];
    // Newest first, so the newest name for an id wins.
    for (const event of events.data ?? []) {
      const sender = event.sender;
      if (sender && sender.kind === kind && sender.id && sender.displayName && !names.has(sender.id)) {
        names.set(sender.id, sender.displayName);
      }
    }
    for (const [id, name] of Object.entries(learned)) names.set(id, name);
    if (me?.id && me.displayName) names.set(me.id, me.displayName);
    return names;
  }, [events.data, learned, me?.id, me?.displayName, integration]);

  const unknown = useMemo(
    () => (integration === "github" ? [...new Set(ids)].filter((id) => isGitHubUserId(id) && !known.has(id)).sort() : []),
    [integration, ids, known],
  );
  // Ask GitHub only once what is known locally has loaded: a name the
  // firings or "Me" already carry costs nothing.
  const settled = !events.isLoading && !me?.loading;
  const lookup = useQuery({
    queryKey: [...triggerKeys.all, "senderNames", integration, unknown] as const,
    queryFn: () => triggerGrpc.resolveSenders(integration, { senderIds: unknown }),
    enabled: settled && unknown.length > 0,
    staleTime: 10 * 60_000,
    // A caller with no GitHub connection gets FailedPrecondition every time;
    // the ids are then shown as themselves rather than retried.
    retry: false,
  });

  const nameOf = useCallback(
    (id: string) => known.get(id) ?? lookup.data?.find((found) => found.query === id)?.displayName,
    [known, lookup.data],
  );
  const remember = useCallback((id: string, name: string) => {
    setLearned((prev) => (prev[id] === name ? prev : { ...prev, [id]: name }));
  }, []);
  return { nameOf, looking: lookup.isFetching, remember };
}
