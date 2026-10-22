// Copyright (c) 2025 Reliant Labs

/**
 * "Runs on": the machine a new chat starts on, or No machine
 * (research/NO_MACHINE_CHATS.md §2.1). The default is decided by the caller
 * (lib/chatMachine.ts defaultChatMachine); this only renders the choice.
 */

import { useEffect, useRef, useState } from "react";
import { Check, ChevronDown, CloudOff, Monitor } from "lucide-react";
import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { cn } from "@/lib/utils";
import {
  chatMachineOptions,
  DEFAULT_MACHINE,
  NO_MACHINE,
  type ChatMachineChoice,
} from "@/lib/chatMachine";
import { Tooltip } from "../ui/Tooltip";
import { machineDisplayName } from "@/lib/machineName";

interface MachinePickerProps {
  value: ChatMachineChoice;
  onChange: (choice: ChatMachineChoice) => void;
  daemons: ReadonlyArray<Pick<DaemonInfo, "daemonId" | "hostname" | "status">>;
  /** The machine DEFAULT_MACHINE resolves to, for its label. */
  defaultDaemon?: Pick<DaemonInfo, "daemonId" | "hostname">;
  disabled?: boolean;
}

export function MachinePicker({ value, onChange, daemons, defaultDaemon, disabled }: MachinePickerProps) {
  const [isOpen, setIsOpen] = useState(false);
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
  const noMachine = value === NO_MACHINE;
  const selectedLabel = noMachine
    ? "No machine"
    : value === DEFAULT_MACHINE
      ? (defaultDaemon ? machineDisplayName(defaultDaemon) : options[0]?.label) || "Your machine"
      : options.find((o) => o.value === value)?.label || "Your machine";

  const choose = (choice: ChatMachineChoice) => {
    onChange(choice);
    setIsOpen(false);
  };

  return (
    <div className="relative" ref={containerRef}>
      <Tooltip
        content={
          noMachine
            ? "No machine: can't read or change files in this project. The chat can use the web and your integrations."
            : "The machine this chat runs on: its files, shell and tools."
        }
        placement="top"
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
          className="absolute top-full left-0 mt-1 border border-border/50 rounded-md elevation-4 z-[1000] min-w-64 bg-[var(--chat-dropdown-bg)] overflow-hidden"
        >
          <div className="overflow-y-auto max-h-60">
            {options.map((option) => {
              const selected =
                value === option.value || (value === DEFAULT_MACHINE && defaultDaemon?.daemonId === option.value);
              return (
                <button
                  key={option.value}
                  type="button"
                  role="option"
                  aria-selected={selected}
                  onClick={() => choose(option.value)}
                  className={cn(
                    "w-full px-3 py-2 text-left text-xs transition-colors border-b border-border/50 hover:bg-muted",
                    selected && "bg-muted",
                  )}
                >
                  <div className="flex items-center justify-between gap-2">
                    <div className="min-w-0">
                      <div className="font-medium truncate">{option.label}</div>
                      <div className="text-xs text-muted-foreground">{option.statusLabel}</div>
                    </div>
                    {selected && <Check className="w-3 h-3 text-primary flex-shrink-0" />}
                  </div>
                </button>
              );
            })}
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
