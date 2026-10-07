// Copyright (c) 2025 Reliant Labs

/**
 * /workflows/automations/$triggerId — one automation: its definition, its controls, and
 * the history of every time it fired.
 *
 * "Run now" is asynchronous on the server: FireTrigger only starts the fire
 * workflow, and the launched (or skipped, or failed) run shows up as a new
 * event a moment later. So after a fire the event list polls briefly until
 * that event lands, rather than leaving the user to refresh.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ArrowRight, Pencil, Play, Trash2 } from "lucide-react";
import { toast } from "sonner";

import Card, { CardHeader, CardInset } from "../forge-ui/card";
import ConfirmationDialog from "../forge-ui/confirmation_dialog";
import { Button } from "../ui/Button";
import { Toggle } from "../ui/Toggle";
import { useProjectStore } from "@/store/projectStore";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import {
  isTriggerNotFound,
  triggerErrorMessage,
  triggerSchedule,
  type Trigger,
  type TriggerEvent,
} from "@/api/trigger-grpc";
import {
  useDeleteTrigger,
  useFireTrigger,
  useSetTriggerEnabled,
  useTrigger,
  useTriggerEvents,
} from "@/hooks/trigger-queries";
import { describeSchedule, describeTriggerSource } from "@/lib/cronText";
import { useIntegrationEventNaming } from "@/hooks/connection-queries";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { AutomationFormDialog } from "./AutomationFormDialog";
import { OutcomeBadge } from "./OutcomeBadge";
import { RunStatusBadge } from "../ui/RunStatusIndicator";
import { useLaunchedRunStatus } from "./useLaunchedRunStatus";
import { automationMachineLabel, daemonStatusLabel } from "./daemonChoices";
import { BrokenActivationNotice } from "./BrokenActivationNotice";
import { runStatusFromDisplayState } from "@/lib/runStatus";
import type { RunDisplayState } from "@/gen/reliant/v1/run_pb";

/** How long to poll for the event a "Run now" produces. */
const FIRE_POLL_MS = 2_000;
const FIRE_POLL_WINDOW_MS = 30_000;

export function AutomationDetailPage() {
  const { triggerId } = useParams({ strict: false }) as { triggerId?: string };
  return triggerId ? <AutomationDetail triggerId={triggerId} /> : <NotFound />;
}

function NotFound() {
  const navigate = useNavigate();
  return (
    <Card padding="lg" role="alert">
      <p className="text-sm font-medium text-foreground">This automation does not exist.</p>
      <p className="mt-1 text-sm text-muted-foreground">It may have been deleted.</p>
      <Button className="mt-4" variant="outline" onClick={() => void navigate({ to: "/workflows/automations" })}>
        All automations
      </Button>
    </Card>
  );
}

export function AutomationDetail({ triggerId }: { triggerId: string }) {
  const navigate = useNavigate();
  const triggerQuery = useTrigger(triggerId);
  const [pollUntil, setPollUntil] = useState<number | null>(null);
  const eventsQuery = useTriggerEvents(triggerId, {
    refetchInterval: pollUntil ? FIRE_POLL_MS : 60_000,
  });
  const fire = useFireTrigger();
  const setEnabled = useSetTriggerEnabled();
  const naming = useIntegrationEventNaming();
  const deleteTrigger = useDeleteTrigger();
  const [editing, setEditing] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const eventCountAtFire = useRef<number | null>(null);

  const projects = useProjectStore((state) => state.projects);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const { daemons } = useDaemonStatus();
  useEffect(() => {
    void loadProjects().catch(() => undefined);
  }, [loadProjects]);

  // Stop polling once the fire's event has arrived, or the window has passed.
  const eventCount = eventsQuery.data?.length ?? 0;
  useEffect(() => {
    if (!pollUntil) return;
    const arrived = eventCountAtFire.current !== null && eventCount > eventCountAtFire.current;
    if (arrived) {
      setPollUntil(null);
      void triggerQuery.refetch();
      return;
    }
    const timer = window.setTimeout(() => setPollUntil(null), Math.max(0, pollUntil - Date.now()));
    return () => window.clearTimeout(timer);
  }, [pollUntil, eventCount, triggerQuery]);

  const trigger = triggerQuery.data;
  const projectName = useMemo(
    () => (trigger ? projects.find((p) => p.id === trigger.projectId)?.name : undefined),
    [projects, trigger],
  );

  if (triggerQuery.isLoading) {
    return (
      <div aria-busy="true" aria-label="Loading automation" className="space-y-4">
        <div className="h-8 w-64 animate-pulse rounded bg-border" />
        <div className="h-40 animate-pulse rounded-lg bg-border/60" />
      </div>
    );
  }
  if (triggerQuery.isError && isTriggerNotFound(triggerQuery.error)) return <NotFound />;
  if (triggerQuery.isError || !trigger) {
    return (
      <Card padding="lg" role="alert">
        <p className="text-sm font-medium text-foreground">This automation could not be loaded.</p>
        <p className="mt-1 text-sm text-muted-foreground">{triggerErrorMessage(triggerQuery.error)}</p>
        <Button className="mt-4" variant="outline" onClick={() => void triggerQuery.refetch()}>
          Try again
        </Button>
      </Card>
    );
  }

  const onRunNow = () => {
    eventCountAtFire.current = eventCount;
    fire.mutate(trigger.id, {
      onSuccess: () => {
        toast.success(`${trigger.name} started`, {
          description: "The run will appear in the history below in a moment.",
        });
        setPollUntil(Date.now() + FIRE_POLL_WINDOW_MS);
      },
      onError: (error) =>
        toast.error(`Could not run ${trigger.name}`, { description: triggerErrorMessage(error) }),
    });
  };

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

  const onDelete = () => {
    deleteTrigger.mutate(trigger.id, {
      onSuccess: () => {
        setConfirmingDelete(false);
        toast.success(`Deleted ${trigger.name}`);
        void navigate({ to: "/workflows/automations" });
      },
      onError: (error) => {
        setConfirmingDelete(false);
        toast.error(`Could not delete ${trigger.name}`, { description: triggerErrorMessage(error) });
      },
    });
  };

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="truncate text-2xl font-semibold tracking-tight text-foreground">{trigger.name}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {projectName ?? "Unknown project"} on{" "}
            {automationMachineLabel(
              trigger,
              daemons.find((d) => d.daemonId === trigger.daemonId),
            )}{" "}
            ·{" "}
            {describeTriggerSource(trigger.source, naming)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            leftIcon={<Play className="h-4 w-4" />}
            onClick={onRunNow}
            loading={fire.isPending}
          >
            Run now
          </Button>
          <Button variant="outline" leftIcon={<Pencil className="h-4 w-4" />} onClick={() => setEditing(true)}>
            Edit
          </Button>
          <Button
            variant="destructive"
            leftIcon={<Trash2 className="h-4 w-4" />}
            onClick={() => setConfirmingDelete(true)}
          >
            Delete
          </Button>
        </div>
      </div>

      {trigger.health.status === "broken" && (
        <BrokenActivationNotice trigger={trigger} onRemoved={() => void navigate({ to: "/workflows/automations" })} />
      )}

      <Card padding="lg">
        <CardHeader
          title="Definition"
          actions={
            <div className="flex items-center gap-3 text-sm text-foreground">
              <span aria-hidden="true">{trigger.enabled ? "Enabled" : "Paused"}</span>
              <Toggle
                checked={trigger.enabled}
                onChange={onToggle}
                disabled={setEnabled.isPending}
                srLabel="Automation enabled"
              />
            </div>
          }
        />
        <DefinitionList trigger={trigger} />
      </Card>

      <Card padding="none">
        <div className="flex items-center justify-between gap-3 border-b border-border px-5 py-4">
          <div>
            <h2 className="text-sm font-semibold text-foreground">History</h2>
            <p className="mt-0.5 text-xs text-muted-foreground">Every time this automation fired, newest first.</p>
          </div>
          {pollUntil && (
            <span className="text-xs text-muted-foreground" role="status">
              Waiting for the run to start…
            </span>
          )}
        </div>
        <EventHistory
          events={eventsQuery.data}
          isLoading={eventsQuery.isLoading}
          error={eventsQuery.isError ? triggerErrorMessage(eventsQuery.error) : undefined}
        />
      </Card>

      <AutomationFormDialog
        open={editing}
        trigger={trigger}
        onClose={() => setEditing(false)}
        onSaved={() => toast.success("Automation saved")}
      />
      <ConfirmationDialog
        open={confirmingDelete}
        title={`Delete ${trigger.name}?`}
        description="It will stop running. Chats it already started are kept."
        confirmLabel="Delete automation"
        loading={deleteTrigger.isPending}
        onConfirm={onDelete}
        onCancel={() => setConfirmingDelete(false)}
      />
    </div>
  );
}

function DefinitionList({ trigger }: { trigger: Trigger }) {
  const schedule = triggerSchedule(trigger);
  const naming = useIntegrationEventNaming();
  const { daemons } = useDaemonStatus();
  const daemon = daemons.find((d) => d.daemonId === trigger.daemonId);
  const inputCount = Object.keys(trigger.presets).length + Object.keys(trigger.params).length;
  const rows: Array<{ label: string; value: React.ReactNode }> = [
    {
      label: "Workflow",
      value: trigger.workflow ? getWorkflowDisplayName(trigger.workflow, true) : "Your default workflow",
    },
    {
      label: "Runs on",
      value: (
        <span>
          {automationMachineLabel(trigger, daemon)}
          <span className="text-muted-foreground">
            {" "}
            (
            {trigger.noMachine
              ? "web & integrations only"
              : daemon
                ? daemonStatusLabel(daemon.status)
                : "not in your daemon list"}
            )
          </span>
        </span>
      ),
    },
    {
      label: schedule ? "Schedule" : "Trigger",
      value: schedule ? (
        <span>
          {describeSchedule(schedule)}
          <span className="mt-0.5 block font-mono text-xs text-muted-foreground">
            {[...schedule.cron, ...(schedule.interval ? [`every ${schedule.interval}`] : [])].join(" · ")}
            {` · ${schedule.timezone}`}
          </span>
        </span>
      ) : (
        describeTriggerSource(trigger.source, naming)
      ),
    },
    {
      label: "Next run",
      value: !trigger.enabled ? (
        "Paused"
      ) : trigger.nextFireAt ? (
        <time dateTime={trigger.nextFireAt}>
          {formatAbsoluteTime(trigger.nextFireAt)} ({formatRelativeTime(trigger.nextFireAt)})
        </time>
      ) : (
        "No upcoming run"
      ),
    },
  ];
  // The overlap policy lives on the schedule source; another kind's is not
  // readable here, so it is not guessed at.
  if (schedule) {
    rows.push({
      label: "If still running",
      value:
        schedule.overlap === "allow"
          ? "Start another run anyway"
          : "Skip — nothing starts while the previous run is active or paused",
    });
  }
  if (inputCount > 0) {
    rows.push({
      label: "Workflow inputs",
      value: `${inputCount} set`,
    });
  }

  return (
    <div className="space-y-4">
      <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-[10rem_1fr]">
        {rows.map((row) => (
          <div key={row.label} className="contents">
            <dt className="text-muted-foreground">{row.label}</dt>
            <dd className="min-w-0 text-foreground">{row.value}</dd>
          </div>
        ))}
      </dl>
      <div>
        <h3 className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Prompt</h3>
        <CardInset padding="md">
          <p className="whitespace-pre-wrap break-words text-sm text-foreground">{trigger.message}</p>
        </CardInset>
      </div>
    </div>
  );
}

/**
 * The status of the run a launched event started. A firing that launched
 * nothing has no run, and says so with a dash rather than borrowing the
 * event's outcome.
 *
 * The event carries the run's display state (G3), so the cell reads it from
 * there; the per-chat read is only for a server that does not send it yet.
 */
function LaunchedRunCell({ chatId, displayState }: { chatId?: string; displayState?: RunDisplayState }) {
  const { status, unavailable } = useLaunchedRunStatus(displayState ? undefined : chatId);
  if (!chatId) return <span className="text-muted-foreground">—</span>;
  if (displayState) return <RunStatusBadge status={runStatusFromDisplayState(displayState)} size="md" />;
  if (unavailable) return <span className="text-xs text-muted-foreground">Unavailable</span>;
  if (!status) {
    return (
      <span className="text-xs text-muted-foreground" aria-busy="true">
        Loading…
      </span>
    );
  }
  return <RunStatusBadge status={status} size="md" />;
}

interface EventHistoryProps {
  events?: TriggerEvent[];
  isLoading: boolean;
  error?: string;
}

function EventHistory({ events, isLoading, error }: EventHistoryProps) {
  if (isLoading) {
    return <p className="px-5 py-6 text-sm text-muted-foreground">Loading history…</p>;
  }
  if (error) {
    return (
      <p className="px-5 py-6 text-sm text-destructive-ink" role="alert">
        History could not be loaded: {error}
      </p>
    );
  }
  if (!events || events.length === 0) {
    return (
      <p className="px-5 py-6 text-sm text-muted-foreground">
        This automation has not run yet. Use Run now to try it.
      </p>
    );
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <caption className="sr-only">Automation run history</caption>
        <thead>
          <tr className="border-b border-border/60 text-left text-xs uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-5 py-2 font-medium">Time</th>
            <th scope="col" className="px-5 py-2 font-medium">Firing</th>
            <th scope="col" className="px-5 py-2 font-medium">Run</th>
            <th scope="col" className="px-5 py-2 font-medium">Detail</th>
            <th scope="col" className="px-5 py-2 font-medium"><span className="sr-only">Run</span></th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border/60">
          {events.map((event) => (
            <tr key={event.id} data-testid={`automation-event-${event.id}`}>
              <td className="whitespace-nowrap px-5 py-3 align-top">
                <time dateTime={event.occurredAt} className="text-foreground">
                  {formatAbsoluteTime(event.occurredAt)}
                </time>
                <span className="block text-xs text-muted-foreground">
                  {formatRelativeTime(event.occurredAt)}
                  {event.manual ? " · Run now" : ""}
                </span>
              </td>
              <td className="px-5 py-3 align-top">
                <OutcomeBadge outcome={event.outcome} />
              </td>
              <td className="px-5 py-3 align-top">
                <LaunchedRunCell
                  chatId={event.outcome === "launched" ? event.chatId : undefined}
                  displayState={event.runDisplayState}
                />
              </td>
              <td className="px-5 py-3 align-top text-muted-foreground">
                {event.outcomeDetail || (event.outcome === "launched" ? "Started a chat" : "—")}
              </td>
              <td className="whitespace-nowrap px-5 py-3 text-right align-top">
                {event.chatId && (
                  <Link
                    to="/workflows/runs/$runId"
                    params={{ runId: event.chatId }}
                    className="inline-flex items-center gap-1 rounded-sm text-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                  >
                    Open run
                    <ArrowRight className="h-3.5 w-3.5" aria-hidden="true" />
                  </Link>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
