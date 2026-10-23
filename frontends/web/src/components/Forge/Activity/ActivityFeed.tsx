// Copyright (c) 2025 Reliant Labs

/**
 * The typed activity stream (ActivityService), as rows: who did what to which
 * object, where, and through what — newest first, a page at a time.
 *
 * One component for both places it is read: the console's Activity page (the
 * project's feed) and an object's History (pre-filtered to one target, e.g. an
 * issue's triage trail). Phrasing is activityText.ts; this is layout.
 */

import { Bot, Database, User } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { useActivity } from "@/hooks/observability-queries";
import type { ActivityEvent } from "@/services/controlPlane/observability/activityApi";
import type { DeliveryScope } from "@/services/controlPlane/observability/deliveryTypes";

import { formatAgo, formatWhen } from "../Observability/format";
import { ObsError, ObsLoadingRows } from "../Observability/ObsStates";
import { activitySentence, activityText } from "./activityText";

export function ActivityRow({ event, now = Date.now() }: { event: ActivityEvent; now?: number }) {
  const t = activityText(event);
  const Icon = event.verb === "query_database" ? Database : event.actor.kind === "user" ? User : Bot;
  return (
    <li className="flex items-start gap-3 px-4 py-2.5" data-testid={`activity-${event.id}`} data-verb={event.verb} aria-label={activitySentence(event)}>
      <Icon className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div className="min-w-0 flex-1 space-y-0.5">
        <p className="text-sm text-foreground">
          <span className="font-medium">{t.actor}</span> {t.action} <span className="font-medium">{t.object}</span>
          {t.tail && (t.tail.startsWith(":") ? <span className="text-foreground">{t.tail}</span> : <> {t.tail}</>)}
          {event.env?.name && event.verb !== "query_database" && <span className="text-muted-foreground"> in {event.env.name}</span>}
        </p>
        {(t.facts.length > 0 || t.via) && (
          <p className="flex flex-wrap gap-x-2 text-xs text-muted-foreground" data-testid="activity-facts">
            {t.facts.map((fact) => (
              <span key={fact} className="font-mono">
                {fact}
              </span>
            ))}
            {t.via && <span>{t.via}</span>}
          </p>
        )}
      </div>
      <time className="shrink-0 text-xs tabular-nums text-muted-foreground" dateTime={new Date(event.atMs).toISOString()} title={formatWhen(event.atMs, now)}>
        {formatAgo(event.atMs, now)}
      </time>
    </li>
  );
}

export function ActivityFeed({
  scope,
  target,
  subject,
  empty,
  testId = "activity-feed",
}: {
  scope: DeliveryScope | null;
  /** One object's history; absent: the scope's whole feed. */
  target?: { kind: string; id: string };
  subject: string;
  /** What to say when nothing happened. */
  empty: string;
  testId?: string;
}) {
  const feed = useActivity(scope, target ?? {});
  const events = feed.data?.pages.flatMap((page) => page.events) ?? [];
  if (feed.isLoading) return <ObsLoadingRows rows={3} testId={`${testId}-loading`} />;
  if (feed.error && !feed.data) {
    return (
      <div className="p-4">
        <ObsError error={feed.error} subject={subject} onRetry={() => void feed.refetch()} retrying={feed.isFetching} />
      </div>
    );
  }
  if (events.length === 0) {
    return (
      <p className="px-4 py-5 text-sm text-muted-foreground" data-testid={`${testId}-empty`}>
        {empty}
      </p>
    );
  }
  return (
    <div data-testid={testId}>
      <ol className="divide-y divide-border/60">
        {events.map((event) => (
          <ActivityRow key={event.id} event={event} />
        ))}
      </ol>
      {feed.hasNextPage && (
        <div className="border-t border-border/60 px-4 py-2">
          <Button variant="ghost" size="sm" onClick={() => void feed.fetchNextPage()} loading={feed.isFetchingNextPage} data-testid={`${testId}-more`}>
            Show older
          </Button>
        </div>
      )}
    </div>
  );
}
