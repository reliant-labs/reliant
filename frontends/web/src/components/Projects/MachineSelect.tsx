// Copyright (c) 2025 Reliant Labs

/**
 * Which machine a folder is on: a compact select over the user's machines,
 * each named the one way machines are named (machineDisplayName) with its
 * registry state, so an offline machine is visibly offline before the user
 * tries to browse it.
 */

import { Monitor } from "lucide-react";
import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { daemonStatusLabel } from "@/components/Automations/daemonChoices";
import { machineDisplayName } from "@/lib/machineName";
import { cn } from "@/lib/utils";

export type SelectableMachine = Pick<DaemonInfo, "daemonId" | "hostname" | "status"> &
  Partial<Pick<DaemonInfo, "name" | "daemonType">>;

export function machineOptionLabel(machine: SelectableMachine, localDaemonId?: string | null): string {
  const name = machineDisplayName(machine);
  const where = machine.daemonId === localDaemonId ? " (this computer)" : "";
  return `${name}${where} · ${daemonStatusLabel(machine.status)}`;
}

interface MachineSelectProps {
  machines: ReadonlyArray<SelectableMachine>;
  value: string | null;
  onChange: (daemonId: string) => void;
  /** The visible label; also the select's accessible name. */
  label?: string;
  /** The machine the desktop app runs, marked "(this computer)". */
  localDaemonId?: string | null;
  disabled?: boolean;
  className?: string;
  id?: string;
}

export function MachineSelect({
  machines,
  value,
  onChange,
  label = "Machine",
  localDaemonId,
  disabled,
  className,
  id = "machine-select",
}: MachineSelectProps) {
  return (
    <div className={cn("flex items-center gap-2", className)}>
      <Monitor className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
      <label htmlFor={id} className="flex-shrink-0 text-sm text-muted-foreground">
        {label}
      </label>
      <select
        id={id}
        value={value ?? ""}
        onChange={(event) => onChange(event.target.value)}
        disabled={disabled || machines.length === 0}
        data-testid="machine-select"
        className="h-8 min-w-0 flex-1 rounded-md border border-input bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60"
      >
        {value === null && (
          <option value="" disabled>
            Choose a machine
          </option>
        )}
        {machines.map((machine) => (
          <option key={machine.daemonId} value={machine.daemonId}>
            {machineOptionLabel(machine, localDaemonId)}
          </option>
        ))}
      </select>
    </div>
  );
}
