// Copyright (c) 2025 Reliant Labs

/**
 * The Automations tab (/workflows/automations): a status overview of what
 * runs by itself, across every project, and whether it is working
 * (automation-ux-spec.md §2.1).
 *
 *   - Counts: need you · on and healthy · paused · the next scheduled run.
 *   - "Needs you": every automation in an attention state, each with its one
 *     call to action as a button on the row.
 *   - Coming up: what fires in the next 24 hours.
 *   - Then one row per automation — a workflow turned on in a project, or a
 *     custom trigger — grouped by workflow or project.
 *
 * It lists only what the user turned on. Discovering what COULD run is the
 * Library's job; showing switched-off workflows here ("Not enabled yet") is
 * what blurred the two tabs, so it is gone.
 */

import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { CalendarClock, Plus } from "lucide-react";

import Card from "../forge-ui/card";
import PageHeader from "../forge-ui/page_header";
import Button from "@/components/forge-ui/button";
import { triggerErrorMessage } from "@/hooks/trigger-model";
import { useTriggers } from "@/hooks/trigger-queries";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { AutomationFormDialog } from "./AutomationFormDialog";
import { AutomationGroups, GroupBySwitch, NeedsYouList } from "./AutomationGroups";
import { ComingUpTimeline } from "./ComingUpTimeline";
import { automationCounts, automationOverview, type AutomationGroupBy } from "./automationGrouping";

export function AutomationsListPage() {
  const triggersQuery = useTriggers();
  const [creating, setCreating] = useState(false);
  const [groupBy, setGroupBy] = useState<AutomationGroupBy>("workflow");

  const triggers = useMemo(() => triggersQuery.data ?? [], [triggersQuery.data]);
  const overview = useMemo(() => automationOverview(triggers, groupBy), [triggers, groupBy]);
  const counts = useMemo(() => automationCounts(overview.all), [overview.all]);

  return (
    <div className="space-y-4">
      <div className="forge-ui">
        <PageHeader
          className=""
          title="Automations"
          subtitle="What runs by itself, across every project, and whether it is working."
          actions={[
            {
              label: "Custom automation",
              variant: "secondary",
              icon: <Plus className="h-4 w-4" aria-hidden="true" />,
              onClick: () => setCreating(true),
            },
          ]}
        />
      </div>

      {triggersQuery.isLoading ? (
        <ListSkeleton />
      ) : triggersQuery.isError ? (
        <Card padding="lg" role="alert">
          <p className="text-sm font-medium text-foreground">Automations could not be loaded.</p>
          <p className="mt-1 text-sm text-muted-foreground">{triggerErrorMessage(triggersQuery.error)}</p>
          <Button className="mt-4" variant="outline" onClick={() => void triggersQuery.refetch()}>
            Try again
          </Button>
        </Card>
      ) : overview.all.length === 0 ? (
        <EmptyAutomations onCreate={() => setCreating(true)} />
      ) : (
        <>
          <CountsStrip counts={counts} />
          <NeedsYouList units={overview.needsYou} />
          <ComingUpTimeline triggers={triggers} />
          {overview.groups.length > 0 && (
            <>
              <div className="flex items-center justify-between gap-3">
                <h2 className="text-sm font-semibold text-foreground">On</h2>
                <GroupBySwitch value={groupBy} onChange={setGroupBy} />
              </div>
              <AutomationGroups groups={overview.groups} />
            </>
          )}
        </>
      )}

      <AutomationFormDialog open={creating} onClose={() => setCreating(false)} />
    </div>
  );
}

/** Four numbers across the top. "Need you" jumps to the list it counts. */
function CountsStrip({ counts }: { counts: ReturnType<typeof automationCounts> }) {
  const cells: Array<{ label: string; value: string; href?: string; title?: string; tone?: string }> = [
    { label: "need you", value: String(counts.needYou), href: counts.needYou > 0 ? "#needs-you" : undefined, tone: counts.needYou > 0 ? "text-warning-ink" : undefined },
    { label: "on and healthy", value: String(counts.healthyOn) },
    { label: "paused", value: String(counts.paused) },
    {
      label: "until the next scheduled run",
      value: counts.nextRunAt ? formatRelativeTime(counts.nextRunAt) : "—",
      title: counts.nextRunAt ? formatAbsoluteTime(counts.nextRunAt) : "Nothing is scheduled",
    },
  ];
  return (
    <Card padding="none" className="overflow-hidden" data-testid="automation-counts">
      <dl className="grid grid-cols-2 divide-border/60 sm:grid-cols-4 sm:divide-x">
        {cells.map((cell) => {
          const body = (
            <>
              <dd className={`text-2xl font-semibold tabular-nums text-foreground ${cell.tone ?? ""}`}>{cell.value}</dd>
              <dt className="mt-0.5 text-xs text-muted-foreground">{cell.label}</dt>
            </>
          );
          return (
            <div key={cell.label} className="flex flex-col-reverse px-4 py-3" title={cell.title}>
              {cell.href ? (
                <a href={cell.href} className="flex flex-col-reverse rounded-sm hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40">
                  {body}
                </a>
              ) : (
                body
              )}
            </div>
          );
        })}
      </dl>
    </Card>
  );
}

function EmptyAutomations({ onCreate }: { onCreate: () => void }) {
  return (
    <Card padding="lg" className="flex flex-col items-center px-6 py-14 text-center" data-testid="automations-empty">
      <div className="flex h-12 w-12 items-center justify-center rounded-full border border-border/60 bg-background text-muted-foreground">
        <CalendarClock className="h-6 w-6" aria-hidden="true" />
      </div>
      <h2 className="mt-4 text-base font-semibold text-foreground">Nothing runs by itself yet.</h2>
      <p className="mt-2 max-w-md text-sm text-muted-foreground">
        Set up a workflow from the Library to run it when something happens — a pull request opens, an issue is labelled — or on a schedule.
        Every run is recorded for you to review.
      </p>
      <div className="mt-6 flex flex-wrap items-center justify-center gap-3">
        <Link
          to="/workflows/library"
          className="inline-flex h-9 items-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          Browse the Library
        </Link>
        <Button variant="outline" leftIcon={<Plus className="h-4 w-4" />} onClick={onCreate}>
          Custom automation
        </Button>
      </div>
    </Card>
  );
}

/** Fixed heights matching the loaded layout, so nothing shifts when data lands. */
function ListSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading automations" className="space-y-6">
      <Card padding="none" className="h-20">
        <div className="h-full animate-pulse bg-border/20 motion-reduce:animate-none" />
      </Card>
      <Card padding="none">
        {[0, 1, 2, 3].map((row) => (
          <div key={row} className="flex items-center gap-4 border-b border-border/60 px-5 py-4 last:border-b-0">
            <div className="h-8 w-8 rounded-md border border-border/60 bg-background" />
            <div className="h-4 w-48 animate-pulse rounded bg-border motion-reduce:animate-none" />
            <div className="h-4 flex-1 animate-pulse rounded bg-border/60 motion-reduce:animate-none" />
            <div className="h-6 w-11 animate-pulse rounded-full bg-border motion-reduce:animate-none" />
          </div>
        ))}
      </Card>
    </div>
  );
}
