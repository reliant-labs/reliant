// Copyright (c) 2025 Reliant Labs

/**
 * /automations — every automation the user owns, across projects.
 *
 * One row per automation, answering the three questions someone opening this
 * page has: what does it do (name, project, schedule in words), is it on
 * (the switch), and is it working (the last firing's outcome, and when it runs
 * next). Everything else, including how each launched run went, is on the
 * automation's own page.
 */

import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { CalendarClock, Plus } from "lucide-react";
import { toast } from "sonner";

import Card from "../forge-ui/card";
import { Button } from "../ui/Button";
import { Toggle } from "../ui/Toggle";
import { useProjectStore } from "@/store/projectStore";
import { triggerErrorMessage, type Trigger } from "@/api/trigger-grpc";
import { useSetTriggerEnabled, useTriggers } from "@/hooks/trigger-queries";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { describeSchedule } from "@/lib/cronText";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";
import { AutomationsShell } from "./AutomationsShell";
import { AutomationFormDialog } from "./AutomationFormDialog";
import { OutcomeBadge } from "./OutcomeBadge";
import { daemonLabel } from "./daemonChoices";

export function AutomationsListPage() {
  const triggersQuery = useTriggers();
  const projects = useProjectStore((state) => state.projects);
  const { daemons } = useDaemonStatus();
  const [creating, setCreating] = useState(false);

  const projectNames = useMemo(
    () => new Map(projects.map((project) => [project.id, project.name])),
    [projects],
  );
  const daemonsById = useMemo(() => new Map(daemons.map((d) => [d.daemonId, d])), [daemons]);

  const triggers = triggersQuery.data ?? [];

  return (
    <AutomationsShell>
      <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">Automations</h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Runs that start on a schedule, with no one typing. Each run opens a new chat you can
            review afterwards.
          </p>
        </div>
        {triggers.length > 0 && (
          <Button variant="primary" leftIcon={<Plus className="h-4 w-4" />} onClick={() => setCreating(true)}>
            New automation
          </Button>
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
      ) : triggers.length === 0 ? (
        <EmptyAutomations onCreate={() => setCreating(true)} />
      ) : (
        <Card padding="none">
          <ul aria-label="Automations" className="divide-y divide-border/60">
            {triggers.map((trigger) => (
              <AutomationRow
                key={trigger.id}
                trigger={trigger}
                projectName={projectNames.get(trigger.projectId)}
                daemonName={daemonLabel(daemonsById.get(trigger.daemonId), trigger.daemonId)}
              />
            ))}
          </ul>
        </Card>
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
      <h2 className="mt-4 text-base font-semibold text-foreground">No automations yet</h2>
      <p className="mt-2 max-w-md text-sm text-muted-foreground">
        An automation runs a workflow for you on a schedule — "every weekday at 9 AM, triage new
        issues" — in one of your projects. Each run starts a chat from your prompt, works
        unattended, and leaves the result for you to read.
      </p>
      <Button className="mt-6" variant="primary" leftIcon={<Plus className="h-4 w-4" />} onClick={onCreate}>
        New automation
      </Button>
    </Card>
  );
}

function ListSkeleton() {
  return (
    <Card padding="none" aria-busy="true" aria-label="Loading automations">
      {[0, 1, 2].map((row) => (
        <div key={row} className="flex items-center gap-4 border-b border-border/60 px-5 py-4 last:border-b-0">
          <div className="h-4 w-48 animate-pulse rounded bg-border" />
          <div className="h-4 flex-1 animate-pulse rounded bg-border/60" />
          <div className="h-6 w-11 animate-pulse rounded-full bg-border" />
        </div>
      ))}
    </Card>
  );
}

interface AutomationRowProps {
  trigger: Trigger;
  projectName?: string;
  daemonName?: string;
}

export function AutomationRow({ trigger, projectName, daemonName }: AutomationRowProps) {
  const setEnabled = useSetTriggerEnabled();
  const scheduleText = trigger.schedule ? describeSchedule(trigger.schedule) : "Unknown source";
  const lastEvent = trigger.lastEvent;

  const onToggle = (enabled: boolean) => {
    setEnabled.mutate(
      { id: trigger.id, enabled },
      {
        onError: (error) =>
          toast.error(`Could not ${enabled ? "enable" : "disable"} ${trigger.name}`, {
            description: triggerErrorMessage(error),
          }),
      },
    );
  };

  return (
    <li
      className={cn(
        "grid grid-cols-[1fr_auto] items-center gap-x-6 gap-y-2 px-5 py-4 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_auto]",
        !trigger.enabled && "text-muted-foreground",
      )}
      data-testid={`automation-row-${trigger.id}`}
    >
      <div className="min-w-0">
        <Link
          to="/automations/$triggerId"
          params={{ triggerId: trigger.id }}
          className="block truncate rounded-sm text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {trigger.name}
        </Link>
        <p className="mt-0.5 truncate text-xs text-muted-foreground">
          {projectName ?? "Unknown project"}
          {daemonName ? ` on ${daemonName}` : ""} · {scheduleText}
        </p>
      </div>

      <div className="order-3 col-span-2 flex min-w-0 items-center gap-2 md:order-none md:col-span-1">
        {/* The last FIRING, not the last run's result: "Launched" says a run
            started, not how it went. The run's own status is on the
            automation's page, which can afford one read per run. */}
        <span className="sr-only">Last firing:</span>
        {lastEvent ? (
          <>
            <OutcomeBadge outcome={lastEvent.outcome} />
            <time
              dateTime={lastEvent.occurredAt}
              title={formatAbsoluteTime(lastEvent.occurredAt)}
              className="truncate text-xs text-muted-foreground"
            >
              {formatRelativeTime(lastEvent.occurredAt)}
            </time>
          </>
        ) : (
          <span className="text-xs text-muted-foreground">Never run</span>
        )}
      </div>

      <div className="order-4 col-span-2 min-w-0 text-xs text-muted-foreground md:order-none md:col-span-1">
        {!trigger.enabled ? (
          "Paused"
        ) : trigger.nextFireAt ? (
          <>
            <span className="sr-only">Next run: </span>
            <span aria-hidden="true">Next </span>
            <time dateTime={trigger.nextFireAt} title={formatAbsoluteTime(trigger.nextFireAt)}>
              {formatRelativeTime(trigger.nextFireAt)}
            </time>
          </>
        ) : (
          "No upcoming run"
        )}
      </div>

      <div className="flex items-center justify-end">
        <Toggle
          checked={trigger.enabled}
          onChange={onToggle}
          disabled={setEnabled.isPending}
          // A switch's name is stable; aria-checked carries on/off.
          srLabel={`${trigger.name} enabled`}
        />
      </div>
    </li>
  );
}
