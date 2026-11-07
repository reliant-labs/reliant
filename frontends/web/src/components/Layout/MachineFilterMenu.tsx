// Copyright (c) 2025 Reliant Labs

/**
 * The sidebar's machine filter: "All machines", or any set of the user's
 * machines (lib/sidebarMachineFilter).
 *
 * A set of toggles rather than a menu of commands, so it is a Popover dialog
 * of real checkboxes: several can be changed in one visit, Tab walks them, and
 * each row carries "Only", which shows that machine alone. The last machine
 * still shown cannot be turned off; a filter that shows nothing is not a view.
 *
 * At rest with All machines it is an icon beside sort and view; filtered, it
 * names what it shows, so a list missing some chats always says why.
 */

import { Check, ChevronDown, Server } from "lucide-react";

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { chatMachineOptions, isAwakeMachine, isStartingMachine } from "../../lib/chatMachine";
import {
  isMachineShown,
  NO_MACHINE,
  onlyMachine,
  toggleMachine,
  type MachineFilter,
} from "../../lib/sidebarMachineFilter";
import { cn } from "../../lib/utils";
import Popover from "../forge-ui/popover";
import Status, { type StatusTone } from "../forge-ui/status";
import Tooltip from "../forge-ui/tooltip";

export const ALL_MACHINES_LABEL = "All machines";
export const NO_MACHINE_LABEL = "No machine";

type FilterMenuMachine = Pick<DaemonInfo, "daemonId" | "hostname" | "status"> &
  Partial<Pick<DaemonInfo, "name" | "daemonType">>;

interface MachineFilterMenuProps {
  /** The effective selection (effectiveMachineFilter): null for All machines. */
  filter: MachineFilter;
  machines: readonly FilterMenuMachine[];
  onChange: (filter: MachineFilter) => void;
  /** Offer "No machine": some listed chat runs with no machine by design. */
  includeNoMachine?: boolean;
  /** Chats waiting on the user, per machine id, shown beside each machine. */
  needsYouByMachine?: ReadonlyMap<string, number>;
}

function statusTone(machine: Pick<DaemonInfo, "status">): StatusTone {
  if (isAwakeMachine(machine)) return "success";
  if (machine.status === DaemonStatus.FAILED) return "danger";
  if (isStartingMachine(machine)) return "warning";
  return "neutral";
}

/** What the trigger says the list shows. */
export function machineFilterLabel(filter: MachineFilter, machines: readonly FilterMenuMachine[]): string {
  if (!filter) return ALL_MACHINES_LABEL;
  if (filter.length === 1) {
    const only = filter[0]!;
    if (only === NO_MACHINE) return NO_MACHINE_LABEL;
    return chatMachineOptions(machines.filter((m) => m.daemonId === only))[0]?.label ?? "1 machine";
  }
  return `${filter.length} machines`;
}

export function MachineFilterMenu({
  filter,
  machines,
  onChange,
  includeNoMachine = false,
  needsYouByMachine,
}: MachineFilterMenuProps) {
  const options = chatMachineOptions(machines);
  const byId = new Map(machines.map((m) => [m.daemonId, m]));
  const rows = [
    ...options.map((option) => ({ id: option.value, label: option.label, statusLabel: option.statusLabel })),
    ...(includeNoMachine ? [{ id: NO_MACHINE, label: NO_MACHINE_LABEL, statusLabel: "" }] : []),
  ];
  const ids = rows.map((row) => row.id);
  const shownCount = ids.filter((id) => isMachineShown(filter, id)).length;
  const label = machineFilterLabel(filter, machines);
  const filtered = filter !== null;

  const needsYou = (id: string) => {
    const count = needsYouByMachine?.get(id);
    return count && count > 0 ? (
      <span className="shrink-0 rounded-full bg-primary/10 px-1.5 text-2xs font-medium text-primary-ink">{count}</span>
    ) : null;
  };

  return (
    <Popover
      align="right"
      label="Filter by machine"
      panelClassName="w-64 py-1"
      trigger={(props) => (
        <Tooltip content={`Machines: ${label}`} side="bottom" delay={300} triggerClassName="flex min-w-0 shrink">
          <button
            type="button"
            {...props}
            data-testid="machine-filter-trigger"
            aria-label={`Filter by machine. Showing ${label}`}
            className={cn(
              "flex h-6 min-w-0 shrink-0 items-center gap-1 rounded-md transition-colors",
              "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              filtered
                ? "max-w-[8rem] bg-primary/10 px-1.5 text-xs font-medium text-primary-ink hover:bg-primary/15"
                : "w-6 justify-center text-muted-foreground hover:bg-muted/60 hover:text-foreground",
            )}
          >
            <Server className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            {filtered && (
              <>
                <span className="min-w-0 truncate" data-testid="machine-filter-label">
                  {label}
                </span>
                <ChevronDown className="h-3 w-3 shrink-0" aria-hidden="true" />
              </>
            )}
          </button>
        </Tooltip>
      )}
    >
      <button
        type="button"
        onClick={() => onChange(null)}
        aria-pressed={!filtered}
        data-testid="machine-filter-all"
        className="flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-xs text-ink transition-colors hover:bg-surface-muted focus:bg-surface-muted focus:outline-none"
      >
        <Check className={cn("h-3.5 w-3.5 shrink-0 text-accent-ink", filtered && "invisible")} aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate">{ALL_MACHINES_LABEL}</span>
      </button>
      <div role="separator" className="my-1 border-t border-border" />
      <fieldset>
        <legend className="px-2.5 py-1 text-xs text-ink-muted">Show chats on</legend>
        {rows.map((row) => {
          const shown = isMachineShown(filter, row.id);
          // The last machine shown stays on: "Only" another one, or All machines.
          const locked = shown && shownCount === 1;
          const machine = byId.get(row.id);
          return (
            <div key={row.id} className="group flex items-center pr-1.5 hover:bg-surface-muted">
              <label
                className={cn(
                  "flex min-w-0 flex-1 items-center gap-2 px-2.5 py-1.5 text-xs text-ink",
                  locked ? "cursor-default" : "cursor-pointer",
                )}
              >
                <input
                  type="checkbox"
                  checked={shown}
                  disabled={locked}
                  onChange={() => onChange(toggleMachine(filter, row.id, ids))}
                  data-testid={`machine-filter-${row.id}`}
                  className="h-3.5 w-3.5 shrink-0 rounded border-border accent-primary"
                />
                {machine ? (
                  <Status tone={statusTone(machine)} label={row.statusLabel} hideLabel size="sm" />
                ) : (
                  <span aria-hidden className="h-1.5 w-1.5 shrink-0" />
                )}
                <span className="min-w-0 flex-1 truncate">{row.label}</span>
                {needsYou(row.id)}
              </label>
              <button
                type="button"
                onClick={() => onChange(onlyMachine(row.id, ids))}
                aria-label={`Show only ${row.label}`}
                data-testid={`machine-filter-only-${row.id}`}
                className={cn(
                  "shrink-0 rounded px-1.5 py-0.5 text-2xs font-medium text-ink-muted transition-opacity",
                  "opacity-0 hover:text-ink focus:opacity-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring group-hover:opacity-100",
                )}
              >
                Only
              </button>
            </div>
          );
        })}
      </fieldset>
    </Popover>
  );
}
