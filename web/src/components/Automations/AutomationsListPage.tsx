// Copyright (c) 2025 Reliant Labs

/**
 * /automations — every automation the user owns, across projects
 * (research/WORKFLOW_UI.md §7.2, decision 10: all projects).
 *
 * The page answers "what will run on its own, is it healthy, and what fires
 * next?": the "Coming up" strip on top for the next 24 hours, then the list
 * grouped by workflow or project, with anything failing, waiting or skipping
 * pinned under "Needs attention".
 */

import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { CalendarClock, Plus } from "lucide-react";

import Card from "../forge-ui/card";
import { Button } from "../ui/Button";
import { triggerErrorMessage } from "@/api/trigger-grpc";
import { useTriggers } from "@/hooks/trigger-queries";
import { AutomationsShell } from "./AutomationsShell";
import { AutomationFormDialog } from "./AutomationFormDialog";
import { AutomationGroups, GroupBySwitch } from "./AutomationGroups";
import { ComingUpTimeline } from "./ComingUpTimeline";
import type { AutomationGroupBy } from "./automationGrouping";

export function AutomationsListPage() {
  const triggersQuery = useTriggers();
  const [creating, setCreating] = useState(false);
  const [groupBy, setGroupBy] = useState<AutomationGroupBy>("workflow");

  const triggers = triggersQuery.data ?? [];
  const hasTriggers = triggers.length > 0;

  return (
    <AutomationsShell>
      <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">Automations</h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Runs that start on a schedule, with no one typing. Each run is recorded so you can
            review it afterwards.
          </p>
        </div>
        {hasTriggers && (
          <div className="flex flex-wrap items-center gap-3">
            <GroupBySwitch value={groupBy} onChange={setGroupBy} />
            <Button variant="primary" leftIcon={<Plus className="h-4 w-4" />} onClick={() => setCreating(true)}>
              New automation
            </Button>
          </div>
        )}
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
      ) : !hasTriggers ? (
        <EmptyAutomations onCreate={() => setCreating(true)} />
      ) : (
        <>
          <ComingUpTimeline triggers={triggers} />
          <AutomationGroups triggers={triggers} groupBy={groupBy} />
        </>
      )}

      <AutomationFormDialog open={creating} onClose={() => setCreating(false)} />
    </AutomationsShell>
  );
}

function EmptyAutomations({ onCreate }: { onCreate: () => void }) {
  return (
    <Card padding="lg" className="flex flex-col items-center px-6 py-14 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-full border border-border/60 bg-background text-muted-foreground">
        <CalendarClock className="h-6 w-6" aria-hidden="true" />
      </div>
      <h2 className="mt-4 text-base font-semibold text-foreground">Nothing runs on its own yet</h2>
      <p className="mt-2 max-w-md text-sm text-muted-foreground">
        An automation runs a workflow on a schedule, with no one typing: "every weekday at 9 AM,
        triage new issues". Each run works unattended and is recorded for you to review.
      </p>
      <div className="mt-6 flex flex-wrap items-center justify-center gap-3">
        <Button variant="primary" leftIcon={<Plus className="h-4 w-4" />} onClick={onCreate}>
          New automation
        </Button>
        <Link
          to="/workflow"
          className="inline-flex h-9 items-center rounded-md border border-border px-4 text-sm font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          Browse workflows
        </Link>
      </div>
    </Card>
  );
}

/** Fixed heights matching the loaded layout, so nothing shifts when data lands. */
function ListSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading automations" className="space-y-6">
      <Card padding="none">
        <div className="border-b border-border/60 px-5 py-3">
          <div className="h-4 w-24 animate-pulse rounded bg-border motion-reduce:animate-none" />
        </div>
        <div className="space-y-2 px-5 py-4">
          {[0, 1, 2].map((lane) => (
            <div key={lane} className="flex items-center gap-4">
              <div className="h-3 w-32 animate-pulse rounded bg-border/80 motion-reduce:animate-none" />
              <div className="h-5 flex-1 rounded-sm border border-border/60 bg-background" />
            </div>
          ))}
        </div>
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
