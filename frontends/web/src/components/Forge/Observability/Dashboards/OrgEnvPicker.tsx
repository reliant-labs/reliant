// Copyright (c) 2025 Reliant Labs

/**
 * The environment picker of an org-scoped dashboard: every environment of the
 * organization, named `project/env`, empty meaning all of them. The console's
 * top bar lists only the route project's environments, so an org dashboard
 * carries its own.
 */

import { useState } from "react";
import { Check, ChevronDown, Server } from "lucide-react";

import { cn } from "@/lib/utils";

import type { ObsEnvironment } from "../obsPlace";
import { Popover } from "../ObsScopeBar";
import { orgScopeLabel, type OrgSelection } from "./orgScope";

export function OrgEnvPicker({ environments, selection, onChange }: { environments: readonly ObsEnvironment[]; selection: OrgSelection; onChange: (names: string[]) => void }) {
  const [open, setOpen] = useState(false);
  const toggle = (name: string) => onChange(selection.names.includes(name) ? selection.names.filter((picked) => picked !== name) : [...selection.names, name]);
  return (
    <Popover
      label="Environments"
      open={open}
      onOpenChange={setOpen}
      testId="dashboard-org-env-picker"
      trigger={
        <>
          <Server className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />
          <span className="text-ink-muted">env</span>
          <span className="font-medium">{orgScopeLabel(selection)}</span>
          <ChevronDown className="h-3 w-3 text-ink-muted" aria-hidden="true" />
        </>
      }
    >
      <fieldset>
        <legend className="px-1 pb-1 text-2xs font-medium uppercase tracking-wide text-ink-muted">Environments across every project</legend>
        <button
          type="button"
          onClick={() => onChange([])}
          className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-ink/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="dashboard-org-env-all"
        >
          <Check className={cn("h-3.5 w-3.5", selection.names.length === 0 ? "text-primary" : "invisible")} aria-hidden="true" />
          Every environment
        </button>
        {environments.map((candidate) => (
          <label key={candidate.id} className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-ink/5">
            <input
              type="checkbox"
              checked={selection.names.includes(candidate.name)}
              onChange={() => toggle(candidate.name)}
              className="h-3.5 w-3.5 accent-[hsl(var(--primary))]"
              data-testid={`dashboard-org-env-option-${candidate.name}`}
            />
            <span className="font-mono">{candidate.name}</span>
          </label>
        ))}
      </fieldset>
    </Popover>
  );
}
