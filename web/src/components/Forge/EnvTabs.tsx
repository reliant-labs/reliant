// Copyright (c) 2025 Reliant Labs

/**
 * The environment tab strip, shared by the status and secrets screens.
 *
 * This replaces Secrets/EnvSelector.tsx, which said the same thing for one
 * screen while ForgeStatusPage hand-rolled a second, differently-styled picker
 * for the other. Two pickers for one concept is how they drifted: the secrets
 * one was a pill row, the status one a bordered-button row, and neither was a
 * tablist you could reach with a keyboard in the same way.
 *
 * The list is NOT hardcoded. The only authority on which environments exist is
 * the topology report, which already enumerates every one forge knows about, so
 * the names come from there. A hardcoded dev/staging/prod triple would be wrong
 * for any project that names its environments differently, and silently wrong —
 * a missing tab, not an error — for a project that has a fourth.
 *
 * While the topology is still loading there is nothing honest to draw, so the
 * strip says it is loading rather than showing a guessed default. If topology
 * came back with no environments at all, the caller handles that: an empty
 * strip is not this component's story to tell.
 *
 * The env names render mono because they are identifiers. The tab chrome around
 * them does not — that is UI, not an identifier.
 */

import { cn } from "@/lib/utils";

export interface EnvTabsProps {
  envs: string[];
  selected: string | null;
  onSelect: (env: string) => void;
  isLoading?: boolean;
}

export function EnvTabs({ envs, selected, onSelect, isLoading }: EnvTabsProps) {
  if (isLoading && envs.length === 0) {
    return (
      <p data-testid="env-selector-loading" className="text-xs text-muted-foreground">
        Reading this project&apos;s environments…
      </p>
    );
  }

  if (envs.length === 0) return null;

  return (
    <div
      role="tablist"
      aria-label="Environment"
      data-testid="env-selector"
      className="flex flex-wrap items-center gap-1 border-b border-border"
    >
      {envs.map((env) => {
        const active = env === selected;
        return (
          <button
            key={env}
            type="button"
            role="tab"
            aria-selected={active}
            data-testid={`env-tab-${env}`}
            onClick={() => onSelect(env)}
            className={cn(
              "-mb-px border-b-2 px-3 py-2 font-mono text-sm transition-colors",
              active
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            )}
          >
            {env}
          </button>
        );
      })}
    </div>
  );
}
