// Copyright (c) 2025 Reliant Labs

/**
 * The environment picker.
 *
 * The list is NOT hardcoded. The secret report is per-environment, and the only
 * authority on which environments exist is the topology report, which already
 * enumerates every one forge knows about — so the names come from there. A
 * hardcoded dev/staging/prod triple would be wrong for any project that names
 * its environments differently, and silently wrong (a missing tab, not an error)
 * for a project that has a fourth.
 *
 * While the topology is still loading there is nothing honest to draw, so the
 * selector says it is loading rather than showing a guessed default. If topology
 * came back with no environments at all, the caller handles that — an empty
 * selector is not this component's story to tell.
 */

import { cn } from "@/lib/utils";

export interface EnvSelectorProps {
  envs: string[];
  selected: string | null;
  onSelect: (env: string) => void;
  isLoading?: boolean;
}

export function EnvSelector({ envs, selected, onSelect, isLoading }: EnvSelectorProps) {
  if (isLoading && envs.length === 0) {
    return (
      <p data-testid="env-selector-loading" className="text-xs text-muted-foreground">
        Reading this project&apos;s environments…
      </p>
    );
  }

  return (
    <div
      role="tablist"
      aria-label="Environment"
      data-testid="env-selector"
      className="flex flex-wrap gap-1.5"
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
              "rounded-full border px-3 py-1 font-mono text-xs transition-colors",
              active
                ? "border-primary bg-primary text-primary-foreground"
                : "border-border bg-transparent text-muted-foreground hover:text-foreground"
            )}
          >
            {env}
          </button>
        );
      })}
    </div>
  );
}
