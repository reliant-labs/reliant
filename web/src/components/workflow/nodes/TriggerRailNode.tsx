/**
 * TriggerRailNode — the builder's entry node, drawn as a lane of trigger
 * cards on the left of the canvas (research/WORKFLOW_EDITOR_UX_REVIEW.md §2 Q1):
 *
 *   TRIGGERS
 *   ┌ Chat ──────────── [on] ┐─┐
 *   └ Starts from a chat     ┘ │
 *   ┌ new-issue ─────────────┐─┤──▶ first step
 *   │ GitHub: issues.opened  │ │
 *   └ ● Active · you         ┘ │
 *   ┌ nightly ───────────────┐─┘
 *   └ ○ Not active [Activate]┘
 *   ┌ ─ + Add trigger ─ ─ ─ ─┐
 *
 * Every card has its own connector into one bus, and the bus is the node's
 * single source handle, so "several ways in, one flow" reads at a glance
 * while the graph keeps exactly one entry edge per entry step.
 *
 * The cards are a PROJECTION of two owners, never graph nodes:
 *   - the definition: the Chat card (`automation_only`, inverted) and each
 *     DECLARED trigger (`triggers:`, research/INTEGRATIONS_V1_BRIEF.md §3a);
 *   - the caller's trigger rows: each declared card's activation state
 *     ("you"), and PERSONAL cards for rows with an inline source running this
 *     workflow in this project.
 *
 * Past four triggers the cards collapse to one line each. Validation findings
 * for a declared trigger (`triggers[i](name).field`) render on its card.
 * In-graph event types (message_created, pre_tool_use, …) stay EventNode.
 */

import { Tooltip } from "../../ui/Tooltip";
import { memo, type MouseEvent, type ReactNode } from "react";
import { Handle, Position, useNodeConnections } from "@xyflow/react";
import { AlertOctagon, AlertTriangle, CalendarClock, Clock, MessageCircle, Plug, Plus, Webhook, Workflow, Zap } from "lucide-react";
import type { NodeExecutionStatus } from "../../../lib/workflow-flow";
import { declaredRailLines, triggerRailLines, type DeclaredRailLine, type TriggerRailLine } from "../../../lib/triggerRail";
import { describeDeclaredSource, findingFieldLabel, integrationOf, sourceCase, type TriggerFinding } from "../../../lib/declaredTriggers";
import { describeSchedule } from "../../../lib/cronText";
import { useTriggers } from "../../../hooks/trigger-queries";
import type { Trigger } from "../../../api/trigger-grpc";
import { cn } from "../../../lib/utils";
import { useTriggerRailContext, type TriggerRailContextValue } from "../TriggerRailContext";
import { buildHandleClassName } from "./NodeStatusWrapper";
import { IntegrationLogo } from "../../icons/IntegrationLogo";
import { NODE_DIMENSIONS } from "../../../lib/workflow-node-dimensions";
import { Toggle } from "../../ui/Toggle";

interface TriggerRailNodeProps {
  data: {
    eventType: string;
    label: string;
    executionStatus?: NodeExecutionStatus;
    layoutDirection?: "horizontal" | "vertical";
  };
  selected?: boolean;
}

/** More triggers than this and the cards collapse to one line each. */
export const COMPACT_AFTER = 4;

// Controls inside a React Flow node must not start a drag or pan, and their
// clicks must not also select the node (which opens the payload panel).
const nodeControlClass = "nodrag nopan";

function stop(handler: () => void) {
  return (event: MouseEvent) => {
    event.stopPropagation();
    handler();
  };
}

const STATE_TEXT: Record<DeclaredRailLine["state"], string> = {
  inactive: "Not active",
  active: "Active",
  paused: "Paused",
  failing: "Failing",
  broken: "Broken",
};

/** The bus the cards connect into, and its width (the column's right padding). */
const BUS = "w-4";

function DeclaredIcon({ line }: { line: DeclaredRailLine }) {
  const kind = sourceCase(line.declared);
  if (kind === "integration") return <IntegrationLogo icon={integrationOf(line.declared)?.integration} size="sm" />;
  const Icon = kind === "schedule" ? CalendarClock : kind === "webhook" ? Webhook : kind === "workflowEvent" ? Workflow : Zap;
  return <Icon className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden />;
}

/**
 * One row of the lane: a card plus its connector into the bus on the right.
 * The connector is drawn from the card's vertical centre, and the bus runs
 * from the first card's centre to the last's, so the handle (at the column's
 * centre) always sits on it.
 */
function LaneRow({ first, last, muted, connect, children }: { first: boolean; last: boolean; muted?: boolean; connect: boolean; children: ReactNode }) {
  const line = muted ? "bg-border/50" : "bg-border";
  return (
    <li className="relative py-1">
      {children}
      {connect && (
        <>
          <span aria-hidden className={cn("pointer-events-none absolute left-full top-1/2 h-px", BUS, line)} />
          {!(first && last) && (
            <span
              aria-hidden
              className={cn("pointer-events-none absolute w-px bg-border", first ? "top-1/2" : "top-0", last ? "bottom-1/2" : "bottom-0")}
              style={{ left: "calc(100% + 1rem - 1px)" }}
            />
          )}
        </>
      )}
    </li>
  );
}

const cardClass = (selected: boolean, compact: boolean) =>
  cn(
    "w-full rounded-lg border bg-card text-left text-xs text-foreground shadow-sm transition-colors",
    compact ? "px-2 py-1" : "px-2.5 py-2",
    selected ? "border-primary ring-2 ring-primary/30" : "border-border",
  );

const interactiveCardClass = (selected: boolean, compact: boolean) =>
  cn(nodeControlClass, cardClass(selected, compact), "hover:border-primary/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring");

function ChatCard({ rail, compact, hasOtherTriggers }: { rail: TriggerRailContextValue | null; compact: boolean; hasOtherTriggers: boolean }) {
  const enabled = rail?.chatEnabled ?? true;
  const canEdit = !!rail?.canEditDefinition;
  return (
    <div data-testid="trigger-card-chat" data-enabled={enabled ? "true" : "false"} className={cn(cardClass(false, compact), !enabled && "opacity-75")}>
      <div className="flex items-center gap-2">
        <MessageCircle className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0 flex-1 truncate font-medium">Chat</span>
        {rail && (
          // A wrapper, so the switch's click neither drags the canvas nor
          // selects the node underneath.
          <span className={cn(nodeControlClass, "inline-flex")} onClick={(event) => event.stopPropagation()}>
            <Toggle
              checked={enabled}
              onChange={(next) => rail.onSetChatEnabled(next)}
              disabled={!canEdit}
              srLabel="Start from chat"
              className="h-5 w-9 scale-90"
            />
          </span>
        )}
      </div>
      {!compact && (
        <p className="mt-0.5 pl-6 text-foreground">
          {enabled ? "Starts from a chat" : "Off: chats can't start it"}
        </p>
      )}
      {!enabled && !hasOtherTriggers && (
        <p role="note" className="mt-0.5 flex items-start gap-1 pl-6 text-xs text-warning-ink">
          <AlertTriangle className="mt-px h-3 w-3 flex-shrink-0" aria-hidden />
          <span>Nothing starts it now. Add a trigger, or turn Chat back on.</span>
        </p>
      )}
    </div>
  );
}

function Findings({ findings }: { findings: TriggerFinding[] }) {
  return (
    <>
      {findings.map((finding, i) => (
        <p key={i} role="note" className="mt-0.5 flex items-start gap-1 pl-6 text-2xs text-warning-ink">
          <AlertTriangle className="mt-px h-3 w-3 flex-shrink-0" aria-hidden />
          <span>
            {finding.field && <span className="font-medium">{findingFieldLabel(finding.field)}: </span>}
            {finding.message}
          </span>
        </p>
      ))}
    </>
  );
}

function StateBadge({ line }: { line: DeclaredRailLine }) {
  const count = line.activations.length;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 font-medium",
        line.state === "broken" || line.state === "failing" ? "text-danger-ink" : line.state === "active" ? "text-success-ink" : "text-foreground",
      )}
    >
      {line.state === "broken" ? (
        <AlertOctagon className="h-3 w-3" aria-hidden />
      ) : (
        <span
          aria-hidden
          className={cn(
            "h-1.5 w-1.5 rounded-full",
            line.state === "active" ? "bg-success" : line.state === "failing" ? "bg-destructive" : line.state === "inactive" ? "border border-muted-foreground" : "bg-muted-foreground",
          )}
        />
      )}
      {STATE_TEXT[line.state]}
      {line.state !== "inactive" && <span className="font-normal text-foreground">· you{count > 1 ? ` ×${count}` : ""}</span>}
    </span>
  );
}

function DeclaredCard({ rail, line, compact }: { rail: TriggerRailContextValue; line: DeclaredRailLine; compact: boolean }) {
  const name = line.declared.name ?? "";
  const findings = rail.findingsFor(line.index, name);
  const sourceText = describeDeclaredSource(line.declared, describeSchedule);
  const unsaved = rail.unsavedDeclared.has(name);
  const selected = rail.selectedDeclared === line.index;
  const stateWords = line.state === "inactive" ? "not active" : `${STATE_TEXT[line.state]} for you`;
  return (
    <div data-testid={`trigger-card-${name}`} data-state={line.state}>
      {/* The card and its Activate share a box, so Activate sits on the
          card's status line whatever notes follow below it. */}
      <div className="relative">
        <button
          type="button"
          onClick={stop(() => rail.onEditDeclared(line.index))}
          aria-label={`${name}, ${sourceText}, ${stateWords}${findings.length ? `, ${findings.length} problem${findings.length > 1 ? "s" : ""}` : ""}. Open trigger`}
          aria-pressed={selected}
          className={interactiveCardClass(selected, compact)}
        >
          <span className="flex items-center gap-2">
            <DeclaredIcon line={line} />
            <span className="min-w-0 flex-1 truncate font-medium">{name}</span>
            {compact && <StateBadge line={line} />}
          </span>
          {!compact && (
            <>
              {/* Information, not decoration: foreground at normal weight, since
                  muted-foreground on a card measures ~4.0:1 in light schemes. */}
              <span className="mt-0.5 block truncate pl-6 text-foreground">{sourceText}</span>
              <span className="mt-1 flex min-h-[1.25rem] items-center pl-6">
                <StateBadge line={line} />
              </span>
            </>
          )}
        </button>
        {!compact && line.state === "inactive" && (
          // Outside the card's button (no nested buttons), placed over its
          // status line.
          <button
            type="button"
            onClick={stop(() => rail.onActivateDeclared(line.index))}
            disabled={!rail.canAddTrigger || unsaved}
            aria-label={`Activate ${name}`}
            className={cn(
              nodeControlClass,
              "absolute bottom-2 right-2.5 rounded-md border border-border bg-background px-1.5 py-0.5 text-xs font-medium text-primary transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:text-muted-foreground disabled:hover:bg-transparent",
            )}
          >
            Activate
          </button>
        )}
      </div>
      {unsaved && line.state === "inactive" && <p className="mt-0.5 pl-6 text-xs text-foreground">Save the workflow to activate it.</p>}
      <Findings findings={findings} />
    </div>
  );
}

function PersonalCard({ rail, line, compact }: { rail: TriggerRailContextValue | null; line: TriggerRailLine; compact: boolean }) {
  const { trigger, scheduleText, health, paused, failing } = line;
  return (
    <Tooltip content={health.detail ?? health.label} placement="bottom" delay={300} wrapperClassName="flex w-full">
      <button
        type="button"
        onClick={stop(() => rail?.onEditTrigger(trigger))}
        data-testid={`trigger-card-personal-${trigger.id}`}
        data-paused={paused ? "true" : undefined}
        aria-label={`${trigger.name}, personal, ${scheduleText}${paused || failing ? `, ${health.label}` : ""}. Edit trigger`}
        className={cn(interactiveCardClass(false, compact), paused && "opacity-60")}
      >
        <span className="flex items-center gap-2">
          {trigger.source.kind === "passthrough" ? (
            <Plug className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden />
          ) : (
            <Clock className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden />
          )}
          <span className="min-w-0 flex-1 truncate font-medium">{trigger.name}</span>
          <span className="flex-shrink-0 rounded border border-border px-1 text-xs text-foreground">Personal</span>
          {failing && <span data-testid="trigger-rail-failing-dot" className="h-1.5 w-1.5 flex-shrink-0 rounded-full bg-destructive" aria-hidden />}
        </span>
        {!compact && <span className="mt-0.5 block truncate pl-6 text-foreground">{scheduleText}</span>}
      </button>
    </Tooltip>
  );
}

function OrphanCard({ rail, orphan, compact }: { rail: TriggerRailContextValue | null; orphan: Trigger; compact: boolean }) {
  return (
    <Tooltip content={orphan.health.lastFailureDetail || "Its declared trigger is gone"} placement="bottom" delay={300} wrapperClassName="flex w-full">
      <button
        type="button"
        onClick={stop(() => rail?.onEditTrigger(orphan))}
        aria-label={`${orphan.name}, broken: ${orphan.health.lastFailureDetail || `activates "${orphan.workflowTrigger}", which this workflow no longer declares`}. Fix`}
        className={interactiveCardClass(false, compact)}
      >
        <span className="flex items-center gap-2">
          <AlertOctagon className="h-4 w-4 flex-shrink-0 text-danger-ink" aria-hidden />
          <span className="min-w-0 flex-1 truncate font-medium">{orphan.name}</span>
        </span>
        {!compact && <span className="mt-0.5 block truncate pl-6 text-danger-ink">Broken: “{orphan.workflowTrigger}” removed</span>}
      </button>
    </Tooltip>
  );
}

export const TriggerRailNode = memo(({ data, selected }: TriggerRailNodeProps) => {
  const { executionStatus, layoutDirection = "horizontal" } = data;
  const rail = useTriggerRailContext();
  const horizontal = layoutDirection !== "vertical";

  const sourceConnections = useNodeConnections({ handleType: "source" });
  const isSourceConnected = sourceConnections.length > 0;

  // Activations can be in any project, so the declared cards read the
  // every-project list; personal cards stay scoped to this project.
  const triggersQuery = useTriggers(undefined, { enabled: !!rail?.workflowRef });
  const all = triggersQuery.data ?? [];
  const personal = rail && triggersQuery.data ? triggerRailLines(all, rail.workflowRef, rail.projectId) : [];
  const declared = rail ? declaredRailLines(rail.declared, all, rail.workflowRef) : { lines: [], orphans: [] };

  const triggerCount = declared.lines.length + declared.orphans.length + personal.length;
  const compact = triggerCount > COMPACT_AFTER;
  const rows: Array<{ key: string; muted?: boolean; node: ReactNode }> = [
    {
      key: "chat",
      muted: rail ? !rail.chatEnabled : false,
      node: <ChatCard rail={rail} compact={compact} hasOtherTriggers={triggerCount > 0} />,
    },
    ...(rail ? declared.lines.map((line) => ({ key: `declared-${line.index}`, node: <DeclaredCard rail={rail} line={line} compact={compact} /> })) : []),
    ...declared.orphans.map((orphan) => ({ key: `orphan-${orphan.id}`, node: <OrphanCard rail={rail} orphan={orphan} compact={compact} /> })),
    ...personal.map((line) => ({ key: `personal-${line.trigger.id}`, muted: line.paused, node: <PersonalCard rail={rail} line={line} compact={compact} /> })),
  ];

  const statusBorder =
    executionStatus === "running" ? "border-primary" : executionStatus === "completed" ? "border-success" : executionStatus === "failed" ? "border-destructive" : "border-border/70";

  return (
    <div
      data-testid="trigger-lane"
      data-compact={compact ? "true" : "false"}
      className={cn("relative rounded-xl border border-dashed p-2", statusBorder, selected && "ring-2 ring-primary/30")}
      style={{ width: NODE_DIMENSIONS.triggerRail.width }}
    >
      <div className="px-1 pb-1 text-xs font-semibold uppercase tracking-wide text-foreground">Triggers</div>

      <ul aria-label="How this workflow starts" className={cn("relative", horizontal && "pr-4")}>
        {rows.map((row, i) => (
          <LaneRow key={row.key} first={i === 0} last={i === rows.length - 1} muted={row.muted} connect={horizontal}>
            {row.node}
          </LaneRow>
        ))}
        {horizontal && (
          <Handle
            type="source"
            position={Position.Right}
            className={buildHandleClassName("primary", isSourceConnected, executionStatus)}
            isConnectable={true}
          />
        )}
      </ul>

      {triggersQuery.isError && (
        <p className="flex items-center gap-2 px-1 py-1 text-xs text-foreground">
          <span className="flex-1 truncate">Couldn't load your activations</span>
          <button
            type="button"
            onClick={stop(() => void triggersQuery.refetch())}
            className={cn(nodeControlClass, "font-medium text-primary hover:underline")}
          >
            Retry
          </button>
        </p>
      )}

      {rail && (
        <div className={cn("pt-1", horizontal && "pr-4")}>
          <button
            type="button"
            onClick={stop(() => rail.onAddTrigger())}
            disabled={!rail.canAddTrigger && !rail.canEditDefinition}
            aria-label="Add trigger"
            className={cn(
              nodeControlClass,
              "flex w-full items-center gap-2 rounded-lg border border-dashed border-border px-2.5 py-1.5 text-xs font-medium text-primary transition-colors hover:border-primary/60 hover:bg-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:text-muted-foreground disabled:hover:bg-transparent",
            )}
          >
            <Plus className="h-3.5 w-3.5 flex-shrink-0" aria-hidden />
            Add trigger
          </button>
          {!rail.canAddTrigger && !rail.canEditDefinition && (
            <p className="px-1 pt-0.5 text-xs text-foreground">Save the workflow to add a trigger.</p>
          )}
        </div>
      )}

      {!horizontal && (
        <Handle
          type="source"
          position={Position.Bottom}
          className={buildHandleClassName("primary", isSourceConnected, executionStatus)}
          isConnectable={true}
        />
      )}
    </div>
  );
});

TriggerRailNode.displayName = "TriggerRailNode";
