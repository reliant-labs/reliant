// Copyright (c) 2025 Reliant Labs

/**
 * Where an automation runs, as one line: "Runs in reliant · on MacBook Pro ·
 * main checkout   Change". The project, machine and workspace are inferred
 * (machineDefault.ts), so in the common case this is a sentence to read, not
 * three fields to fill; it opens into pickers on Change, and opens by itself
 * when there is nothing sensible to infer or the choice is invalid.
 */

import { useId, useState } from "react";

import { useWorktreeById } from "@/hooks/worktree-queries";
import { cn } from "@/lib/utils";
import { AddMachineSheet, type ReadyMachine } from "../../Machines";
import { CardInset } from "../../forge-ui/card";
import { WorktreeField } from "../../workflow/run/RunWorkflowForm";
import { errorTextClass, fieldClass, hintClass, labelClass } from "../../workflow/run/runFormStyles";
import { NO_MACHINE, type DaemonChoice } from "../daemonChoices";
import { machineOptionLabel, NO_MACHINE_OPTION_LABEL } from "./machineDefault";

export interface WhereSummaryProps {
  /** Field ids: `${idPrefix}-project`, `-machine`, `-worktree`. */
  idPrefix: string;
  projects: ReadonlyArray<{ id: string; name: string }>;
  projectId: string;
  /** Unset: the project is fixed (settings of an existing automation). */
  onProjectChange?: (projectId: string) => void;
  machine: string;
  onMachineChange: (machine: string) => void;
  choices: readonly DaemonChoice[];
  daemonsLoading: boolean;
  /** The person has at least one machine. */
  hasDaemons: boolean;
  /** Why No machine cannot be chosen; undefined when it can. */
  noMachineBlocker?: string;
  /** The workflow works through its integrations and needs no machine. */
  needsNoMachine?: boolean;
  /** The workflow edits code (the server's needs_machine). */
  needsMachine?: boolean;
  worktreeId?: string;
  onWorktreeChange: (worktreeId: string | undefined) => void;
  projectError?: string;
  machineError?: string;
  onBlurField?: (key: "project" | "machine") => void;
}

export function WhereSummary(props: WhereSummaryProps) {
  const {
    idPrefix,
    projects,
    projectId,
    onProjectChange,
    machine,
    onMachineChange,
    choices,
    daemonsLoading,
    hasDaemons,
    noMachineBlocker,
    needsNoMachine,
    needsMachine,
    worktreeId,
    onWorktreeChange,
    projectError,
    machineError,
    onBlurField,
  } = props;
  const panelId = useId();
  const [open, setOpen] = useState(false);
  const [adding, setAdding] = useState(false);
  const id = (name: string) => `${idPrefix}-${name}`;

  const projectName = projects.find((p) => p.id === projectId)?.name ?? "this project";
  const noMachine = machine === NO_MACHINE;
  const chosen = choices.find((c) => c.daemonId === machine);
  const worktree = useWorktreeById(worktreeId);

  const noMachineOffered = !noMachineBlocker || noMachine;
  const unresolved = !machine || !!machineError || !!projectError || (!daemonsLoading && !hasDaemons && !noMachineOffered);
  // Open on its own while there is something to decide, so a blank or
  // refused choice is never hidden behind "Change".
  const expanded = open || unresolved;

  const machineText = noMachine
    ? "with no machine"
    : chosen
      ? `on ${chosen.label}`
      : machine
        ? `on machine ${machine.slice(0, 8)}`
        : daemonsLoading
          ? "on …"
          : "on no machine yet";
  const workspaceText = noMachine ? null : worktreeId ? (worktree?.name ?? "a branch workspace") : "main checkout";

  const machineHint = noMachine
    ? needsNoMachine
      ? "This workflow needs none: it works through its integrations."
      : "Runs use only the web, integrations and planning tools; nothing touches files."
    : needsMachine
      ? "It edits code, so it needs a machine with your checkout."
      : "Its tools run on this machine. If it's asleep when the automation fires, Reliant wakes it.";

  return (
    <div className="space-y-3" data-testid="where-summary">
      <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm">
        <p className="min-w-0 text-foreground" aria-live="polite">
          Runs in <span className="font-medium">{projectName}</span> ·{" "}
          <span className="font-medium">{machineText}</span>
          {workspaceText && (
            <>
              {" "}
              · <span className="text-muted-foreground">{workspaceText}</span>
            </>
          )}
        </p>
        {!unresolved && (
          <button
            type="button"
            aria-expanded={expanded}
            aria-controls={panelId}
            onClick={() => setOpen((o) => !o)}
            className="rounded-sm text-xs font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            {open ? "Done" : "Change"}
          </button>
        )}
      </div>

      <CardInset id={panelId} hidden={!expanded} className="space-y-4">
        <div className={cn("grid gap-4", onProjectChange && projects.length > 1 && "sm:grid-cols-2")}>
          {onProjectChange && projects.length > 1 && (
            <div onBlur={() => onBlurField?.("project")}>
              <label htmlFor={id("project")} className={labelClass}>
                Project
              </label>
              <select
                id={id("project")}
                className={fieldClass}
                value={projectId}
                onChange={(e) => onProjectChange(e.target.value)}
                aria-invalid={!!projectError}
                aria-describedby={projectError ? id("project-error") : undefined}
              >
                {projects.map((project) => (
                  <option key={project.id} value={project.id}>
                    {project.name}
                  </option>
                ))}
              </select>
              {projectError && <p id={id("project-error")} className={errorTextClass}>{projectError}</p>}
            </div>
          )}
          <div onBlur={() => onBlurField?.("machine")}>
            {!daemonsLoading && !hasDaemons && !machine ? (
              <span id={id("machine-label")} className={labelClass}>
                Runs on
              </span>
            ) : (
              <label htmlFor={id("machine")} className={labelClass}>
                Runs on
              </label>
            )}
            {!daemonsLoading && !hasDaemons && !machine ? (
              <div
                id={id("machine")}
                role="group"
                aria-labelledby={id("machine-label")}
                aria-invalid={!!machineError}
                aria-describedby={machineError ? id("machine-error") : undefined}
                tabIndex={-1}
                className="rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-muted-foreground focus:outline-none"
              >
                You have no machine yet. Runs that edit code need one.{" "}
                <button
                  type="button"
                  onClick={() => setAdding(true)}
                  className="rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                >
                  Set up a machine
                </button>
                {noMachineOffered && (
                  <>
                    , or{" "}
                    <button
                      type="button"
                      onClick={() => onMachineChange(NO_MACHINE)}
                      className="rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                    >
                      choose No machine
                    </button>{" "}
                    if it only uses the web and integrations
                  </>
                )}
                .
              </div>
            ) : (
              <MachineSelect
                id={id("machine")}
                value={machine}
                loading={daemonsLoading}
                choices={choices}
                onChange={onMachineChange}
                invalid={!!machineError}
                noMachineBlocker={noMachine ? undefined : noMachineBlocker}
              />
            )}
            {(hasDaemons || machine) && <p className={hintClass}>{machineHint}</p>}
            {machineError && (
              <p id={id("machine-error")} className={errorTextClass} role="alert">
                {machineError}
              </p>
            )}
          </div>
        </div>
        {machine && !noMachine && projectId && (
          <div>
            <WorktreeField
              id={id("worktree")}
              projectId={projectId}
              worktreeId={worktreeId}
              disabled={false}
              onChange={onWorktreeChange}
            />
          </div>
        )}
      </CardInset>

      {/* Kept mounted while closed, so a machine still provisioning after
          "Keep working" reports in; the new machine is the one it runs on. */}
      <AddMachineSheet
        open={adding}
        onClose={() => setAdding(false)}
        onReady={(ready: ReadyMachine) => {
          setAdding(false);
          onMachineChange(ready.daemonId);
        }}
      />
    </div>
  );
}

export interface MachineSelectProps {
  id: string;
  value: string;
  loading: boolean;
  choices: readonly DaemonChoice[];
  onChange: (value: string) => void;
  invalid?: boolean;
  /** Offered as the empty option (an override that follows the shared choice). */
  inheritLabel?: string;
  /** Why No machine is unavailable: the option is shown disabled, with the reason. */
  noMachineBlocker?: string;
}

/** Runs on: every machine, then "No machine", always last (report 03 §7). */
export function MachineSelect({ id, value, loading, choices, onChange, invalid, inheritLabel, noMachineBlocker }: MachineSelectProps) {
  return (
    <select id={id} className={fieldClass} value={value} onChange={(e) => onChange(e.target.value)} aria-invalid={invalid} aria-describedby={invalid ? `${id}-error` : undefined}>
      <option value="" disabled={!inheritLabel}>
        {inheritLabel ?? (loading ? "Loading machines…" : "Choose a machine")}
      </option>
      {choices.map((choice) => (
        <option key={choice.daemonId} value={choice.daemonId} disabled={!choice.eligible}>
          {machineOptionLabel(choice)}
        </option>
      ))}
      <option value={NO_MACHINE} disabled={!!noMachineBlocker}>
        {noMachineBlocker ? `${NO_MACHINE_OPTION_LABEL}: ${noMachineBlocker}` : NO_MACHINE_OPTION_LABEL}
      </option>
    </select>
  );
}
