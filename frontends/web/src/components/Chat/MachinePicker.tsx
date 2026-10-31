// Copyright (c) 2025 Reliant Labs

/**
 * "Runs on": the machine a new chat starts on, or No machine
 * (research/NO_MACHINE_CHATS.md §2.1). The default is decided by the caller
 * (lib/chatMachine.ts defaultChatMachine); this only renders the choice, and
 * offers "Make default" on each machine (DaemonInfo.is_default) when the
 * caller wires onMakeDefault.
 *
 * Given the new chat's project (`project`), the list is split by where that
 * project is set up (lib/projectPlacement.ts): the machines that have it come
 * first, marked ready (or still setting up), each with its online/offline
 * state; every other machine sits under "Not set up for <project>" and cannot
 * be chosen as it is. Its row offers "Set up <project> on <machine>" instead,
 * which clones the project there and then chooses it. So the picker never
 * offers a project/machine pair that would fail.
 */

import { useEffect, useRef, useState } from "react";
import { AlertCircle, Check, ChevronDown, CloudOff, Download, Loader2, Monitor } from "lucide-react";
import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { cn } from "@/lib/utils";
import {
  chatMachineOptions,
  DEFAULT_MACHINE,
  NO_MACHINE,
  type ChatMachineChoice,
  type ChatMachineOption,
} from "@/lib/chatMachine";
import { sectionMachines, type PlacementState, type ProjectPlacement } from "@/lib/projectPlacement";
import Tooltip from "../forge-ui/tooltip";
import { machineDisplayName } from "@/lib/machineName";

/** The new chat's project, and how to put it on a machine that lacks it. */
export interface MachinePickerProject {
  name: string;
  placement: ProjectPlacement;
  /**
   * Set the project up on a machine (clone it there). Absent when it cannot
   * be: a plain folder has nothing to clone from.
   */
  onSetUp?: (daemonId: string) => Promise<void>;
  /** A failed set-up, in words; the caller owns the copy (describeCloneError). */
  describeError?: (err: unknown, machineName: string) => string;
}

interface MachinePickerProps {
  value: ChatMachineChoice;
  onChange: (choice: ChatMachineChoice) => void;
  daemons: ReadonlyArray<Pick<DaemonInfo, "daemonId" | "hostname" | "status"> & Partial<Pick<DaemonInfo, "isDefault">>>;
  /** The machine DEFAULT_MACHINE resolves to, for its label. */
  defaultDaemon?: Pick<DaemonInfo, "daemonId" | "hostname">;
  /** Make a machine the user's default (SetDefaultDaemon). Absent: not offered. */
  onMakeDefault?: (daemonId: string) => void;
  /** Group machines by whether this project is on them. Absent, or not known anywhere: one flat list. */
  project?: MachinePickerProject;
  disabled?: boolean;
}

const SECTION_LABEL = "px-3 pb-1 pt-2 text-2xs font-medium uppercase tracking-wide text-muted-foreground";

function placementLabel(state: PlacementState): string {
  switch (state) {
    case "ready":
      return "Ready";
    case "setting_up":
      return "Setting up…";
    case "failed":
      return "Set-up failed";
    case "absent":
      return "Not set up";
  }
}

export function MachinePicker({
  value,
  onChange,
  daemons,
  defaultDaemon,
  onMakeDefault,
  project,
  disabled,
}: MachinePickerProps) {
  const [isOpen, setIsOpen] = useState(false);
  // The machine a set-up was started on from this picker, and why the last
  // one was refused (the clone was never queued).
  const [settingUp, setSettingUp] = useState<string | null>(null);
  const [setUpError, setSetUpError] = useState<{ daemonId: string; message: string } | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };
    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen]);

  const options = chatMachineOptions(daemons);
  const userDefaultId = daemons.find((d) => d.isDefault)?.daemonId;
  const noMachine = value === NO_MACHINE;
  const selectedLabel = noMachine
    ? "No machine"
    : value === DEFAULT_MACHINE
      ? (defaultDaemon ? machineDisplayName(defaultDaemon) : options[0]?.label) || "Your machine"
      : options.find((o) => o.value === value)?.label || "Your machine";
  const placement = project?.placement.known ? project.placement : undefined;
  const sections = placement
    ? sectionMachines(options, (o) => o.value, placement)
    : { ready: options, notSetUp: [] as ChatMachineOption[] };
  const failedMachine = (daemonId: string) =>
    daemons.find((d) => d.daemonId === daemonId)?.status === DaemonStatus.FAILED;

  const choose = (choice: ChatMachineChoice) => {
    onChange(choice);
    setIsOpen(false);
  };

  const setUp = async (option: ChatMachineOption) => {
    if (!project?.onSetUp || settingUp) return;
    setSettingUp(option.value);
    setSetUpError(null);
    try {
      await project.onSetUp(option.value);
      // Queued: the machine now has the project on its way, so it is a
      // machine this chat can wait for. Choose it.
      choose(option.value);
    } catch (err) {
      setSetUpError({
        daemonId: option.value,
        message: project.describeError?.(err, option.label) ?? (err instanceof Error ? err.message : "Set-up failed"),
      });
    } finally {
      setSettingUp(null);
    }
  };

  const renderChoosable = (option: ChatMachineOption) => {
    const selected =
      value === option.value || (value === DEFAULT_MACHINE && defaultDaemon?.daemonId === option.value);
    const isUserDefault = option.value === userDefaultId;
    const state = placement?.stateOn(option.value);
    return (
      <div
        key={option.value}
        className={cn("group flex items-center border-b border-border/50 hover:bg-muted", selected && "bg-muted")}
      >
        <button
          type="button"
          role="option"
          aria-selected={selected}
          onClick={() => choose(option.value)}
          data-testid={`machine-option-${option.value}`}
          className="min-w-0 flex-1 px-3 py-2 text-left text-xs transition-colors"
        >
          <div className="flex items-center justify-between gap-2">
            <div className="min-w-0">
              <div className="flex items-center gap-1.5 font-medium">
                <span className="truncate">{option.label}</span>
                {isUserDefault && (
                  <span className="flex-shrink-0 text-2xs font-normal text-muted-foreground">Default</span>
                )}
              </div>
              <div className="flex items-center gap-1 text-xs text-muted-foreground">
                {state === "ready" && (
                  <span className="font-medium text-success-ink">{placementLabel(state)}</span>
                )}
                {state === "setting_up" && (
                  <span className="inline-flex items-center gap-1 font-medium text-foreground/80">
                    <Loader2 className="h-3 w-3 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                    {placementLabel(state)}
                  </span>
                )}
                {state && <span aria-hidden="true">·</span>}
                <span>{option.statusLabel}</span>
              </div>
            </div>
            {selected && <Check className="w-3 h-3 text-primary flex-shrink-0" />}
          </div>
        </button>
        {onMakeDefault && !isUserDefault && (
          <button
            type="button"
            onClick={() => onMakeDefault(option.value)}
            aria-label={`Make ${option.label} your default machine`}
            className="mr-2 flex-shrink-0 rounded px-1.5 py-0.5 text-2xs text-muted-foreground opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
          >
            Make default
          </button>
        )}
      </div>
    );
  };

  // A machine without the project: not a choice as it is, so it is an
  // option only in name (aria-disabled), with the way to make it one.
  const renderNotSetUp = (option: ChatMachineOption) => {
    const state = placement?.stateOn(option.value) ?? "absent";
    const busy = settingUp === option.value;
    const error = setUpError?.daemonId === option.value ? setUpError.message : null;
    const canSetUp = Boolean(project?.onSetUp) && !failedMachine(option.value);
    const action = state === "failed" ? `Try setting up ${project?.name} on ${option.label} again` : `Set up ${project?.name} on ${option.label}`;
    return (
      <div key={option.value} className="border-b border-border/50 px-3 py-2 text-xs" data-testid={`machine-not-set-up-${option.value}`}>
        <div role="option" aria-selected={false} aria-disabled="true" className="min-w-0 text-muted-foreground">
          <div className="truncate font-medium text-foreground/70">{option.label}</div>
          <div className="flex items-center gap-1">
            {state === "failed" && <AlertCircle className="h-3 w-3 text-destructive-ink" aria-hidden="true" />}
            <span className={cn(state === "failed" && "text-destructive-ink")}>{placementLabel(state)}</span>
            <span aria-hidden="true">·</span>
            <span>{option.statusLabel}</span>
          </div>
        </div>
        {canSetUp && (
          <button
            type="button"
            onClick={() => void setUp(option)}
            disabled={Boolean(settingUp)}
            data-testid={`machine-set-up-${option.value}`}
            className="mt-1.5 inline-flex max-w-full items-center gap-1.5 rounded-md border border-border/70 px-2 py-1 font-medium text-primary transition-colors hover:border-border hover:bg-muted disabled:opacity-60"
          >
            {busy ? (
              <Loader2 className="h-3 w-3 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />
            ) : (
              <Download className="h-3 w-3 shrink-0" aria-hidden="true" />
            )}
            <span className="truncate">{action}</span>
          </button>
        )}
        {error && (
          <p role="alert" className="mt-1 text-destructive-ink">
            {error}
          </p>
        )}
      </div>
    );
  };

  return (
    <div className="relative" ref={containerRef}>
      <Tooltip
        content={
          noMachine
            ? "No machine: can't read or change files in this project. The chat can use the web and your integrations."
            : "The machine this chat runs on: its files, shell and tools."
        }
        side="top"
      >
        <button
          type="button"
          onClick={() => setIsOpen(!isOpen)}
          disabled={disabled}
          aria-haspopup="listbox"
          aria-expanded={isOpen}
          aria-label={`Runs on: ${selectedLabel}`}
          data-testid="machine-picker"
          className="flex items-center gap-2 px-5 py-2.5 text-sm font-medium text-foreground bg-background border border-border/70 rounded-lg hover:border-border transition-colors disabled:opacity-60"
        >
          {noMachine ? <CloudOff className="w-4 h-4" /> : <Monitor className="w-4 h-4" />}
          <span className="max-w-[180px] truncate">{selectedLabel}</span>
          <ChevronDown className="w-3.5 h-3.5 opacity-60" />
        </button>
      </Tooltip>

      {isOpen && (
        <div
          role="listbox"
          aria-label="Runs on"
          className="absolute top-full left-0 mt-1 border border-border/50 rounded-md elevation-4 z-[1000] min-w-72 max-w-sm bg-[var(--chat-dropdown-bg)] overflow-hidden text-left"
        >
          <div className="overflow-y-auto max-h-80">
            {placement ? (
              <>
                {sections.ready.length > 0 && (
                  <div role="group" aria-label={`Set up for ${project?.name}`}>
                    <div className={SECTION_LABEL} aria-hidden="true">
                      Set up for {project?.name}
                    </div>
                    {sections.ready.map(renderChoosable)}
                  </div>
                )}
                {sections.notSetUp.length > 0 && (
                  <div role="group" aria-label={`Not set up for ${project?.name}`}>
                    <div className={SECTION_LABEL} aria-hidden="true">
                      Not set up for {project?.name}
                    </div>
                    {!project?.onSetUp && (
                      <p className="px-3 pb-1.5 text-xs text-muted-foreground">
                        {project?.name} is a folder on another machine, so it can&apos;t be cloned here.
                      </p>
                    )}
                    {sections.notSetUp.map(renderNotSetUp)}
                  </div>
                )}
              </>
            ) : (
              options.map(renderChoosable)
            )}
            <button
              type="button"
              role="option"
              aria-selected={noMachine}
              onClick={() => choose(NO_MACHINE)}
              className={cn("w-full px-3 py-2 text-left text-xs transition-colors hover:bg-muted", noMachine && "bg-muted")}
            >
              <div className="flex items-center justify-between gap-2">
                <div className="min-w-0">
                  <div className="font-medium">No machine</div>
                  <div className="text-xs text-muted-foreground">Web &amp; integrations · can&apos;t read or change files</div>
                </div>
                {noMachine && <Check className="w-3 h-3 text-primary flex-shrink-0" />}
              </div>
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
