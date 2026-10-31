// Copyright (c) 2025 Reliant Labs

/**
 * Inline rename for a machine: the machine row and the detail header both
 * swap the name for this field. DaemonRegistryService.RenameMachine writes
 * the control plane (which owns the name) and then the registry row, and its
 * generated mutation invalidates the one shared daemon list, so the pickers,
 * triggers and every other reader show the new name on their next render.
 *
 * The name is a label only — the machine's id and its workspace never
 * change — so renaming a running machine is safe and needs no restart.
 */
import { useEffect, useRef, useState } from "react";
import { Check, Pencil, X } from "lucide-react";

import { useRenameMachine } from "@/hooks/daemon-registry-service-hooks_gen";
import Button from "@/components/forge-ui/button";
import { cn } from "@/lib/utils";
import { describeError } from "@/services/controlPlane/environments";

/** The control plane's rule for a machine name (svcdaemon isValidName). */
const MACHINE_NAME_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

export const MACHINE_NAME_RULE = "Use 1–63 lowercase letters, digits or hyphens, starting and ending with a letter or digit.";

/** Why `name` cannot be a machine name, or null when it can. */
export function machineNameProblem(name: string): string | null {
  return MACHINE_NAME_RE.test(name.trim()) ? null : MACHINE_NAME_RULE;
}

/** The pencil beside a machine's name that starts a rename. */
export function RenameMachineButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button
      variant="ghost"
      size="md"
      onClick={onClick}
      aria-label={`Rename ${label}`}
      title="Rename machine"
      icon={<Pencil className="h-4 w-4" aria-hidden="true" />}
    />
  );
}

export function RenameMachineField({
  daemonId,
  currentName,
  onDone,
  className,
}: {
  daemonId: string;
  /** The name to start from: the machine's own name, or what it is shown as. */
  currentName: string;
  /** Called after a save or a cancel. */
  onDone: () => void;
  className?: string;
}) {
  const [value, setValue] = useState(currentName);
  const [error, setError] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const rename = useRenameMachine({
    // Rendered inline below the field, where the user is looking.
    meta: { silenceErrorToast: true },
    onSuccess: () => onDone(),
    onError: (err) => setError(describeError(err, "Failed to rename the machine")),
  });

  useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();
  }, []);

  const save = () => {
    const name = value.trim();
    if (name === currentName) {
      onDone();
      return;
    }
    const problem = machineNameProblem(name);
    if (problem) {
      setError(problem);
      return;
    }
    setError("");
    rename.mutate({ daemonId, name });
  };

  return (
    <form
      className={cn("flex flex-col gap-1", className)}
      onSubmit={(e) => {
        e.preventDefault();
        save();
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          onDone();
        }
      }}
      data-testid="rename-machine-form"
    >
      <div className="flex items-center gap-1">
        <input
          ref={inputRef}
          value={value}
          onChange={(e) => {
            setValue(e.target.value);
            if (error) setError("");
          }}
          aria-label="Machine name"
          aria-invalid={error ? true : undefined}
          maxLength={63}
          spellCheck={false}
          autoCapitalize="off"
          autoComplete="off"
          disabled={rename.isPending}
          className="w-48 rounded-md border border-border bg-background px-2 py-1 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
        />
        <Button
          type="submit"
          variant="ghost"
          size="md"
          disabled={rename.isPending}
          aria-label="Save name"
          title="Save"
          icon={<Check className="h-4 w-4" aria-hidden="true" />}
        />
        <Button
          type="button"
          variant="ghost"
          size="md"
          disabled={rename.isPending}
          onClick={onDone}
          aria-label="Cancel rename"
          title="Cancel"
          icon={<X className="h-4 w-4" aria-hidden="true" />}
        />
      </div>
      {error && (
        <p role="alert" className="max-w-xs whitespace-normal text-xs text-destructive-ink">
          {error}
        </p>
      )}
    </form>
  );
}
