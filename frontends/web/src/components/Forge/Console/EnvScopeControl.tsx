// Copyright (c) 2025 Reliant Labs

/**
 * The top bar's environment scope (decision D1): a segmented control of the
 * long-lived environments, All, and the previews behind one menu.
 *
 * On a page that pins its own environment (one environment's settings) the
 * control stays, dimmed, and says so: choosing here still sets the scope the
 * list pages open with, it just does not move this page.
 */

import { useEffect, useRef, useState } from "react";
import { ChevronDown } from "lucide-react";

import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import { cn } from "@/lib/utils";

import { ENV_ALL, type ScopeOption } from "./envScope";

export interface EnvScopeControlProps {
  value: string;
  long: ScopeOption[];
  previews: ScopeOption[];
  onChange: (env: string) => void;
  /** The environment this page's path names, when it names one. */
  pinnedEnv?: string | null;
}

/** More long-lived environments than this and the rest join the menu. */
const MAX_SEGMENTS = 5;

const segment =
  "inline-flex h-6 items-center gap-1 whitespace-nowrap rounded px-2.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent";

export function EnvScopeControl({ value, long, previews, onChange, pinnedEnv }: EnvScopeControlProps) {
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const escapeLayer = useEscapeLayer(menuOpen, () => setMenuOpen(false));

  // Dismiss on an outside click (the DROPDOWN_STANDARDS pattern).
  useEffect(() => {
    if (!menuOpen) return;
    const onPointerDown = (event: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) setMenuOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [menuOpen]);

  const segments = long.slice(0, MAX_SEGMENTS);
  const overflow = [...long.slice(MAX_SEGMENTS), ...previews];
  const inMenu = overflow.some((option) => option.name === value);
  const menuLabel = inMenu
    ? value
    : previews.length > 0 && long.length <= MAX_SEGMENTS
      ? `${previews.length} ${previews.length === 1 ? "preview" : "previews"}`
      : "More";

  const choose = (env: string) => {
    setMenuOpen(false);
    onChange(env);
  };

  return (
    <div
      role="group"
      aria-label="Environment scope"
      title={
        pinnedEnv
          ? `This page is pinned to ${pinnedEnv}. The scope applies to list pages.`
          : "Environment scope, remembered for this project"
      }
      data-testid="env-scope"
      data-pinned={pinnedEnv ? "true" : undefined}
      className={cn(
        "relative inline-flex items-center gap-0.5 rounded-md border border-border bg-surface p-0.5",
        pinnedEnv && "opacity-60"
      )}
    >
      {segments.map((option) => (
        <button
          key={option.name}
          type="button"
          aria-pressed={value === option.name}
          onClick={() => choose(option.name)}
          data-testid={`env-scope-${option.name}`}
          className={cn(segment, value === option.name ? "bg-ink/8 font-medium text-ink" : "text-ink-muted hover:text-ink")}
          title={option.recorded ? undefined : `${option.name} is declared in your checkout; Reliant has no record of it yet`}
        >
          {option.name}
        </button>
      ))}
      <span aria-hidden="true" className="mx-0.5 h-4 w-px bg-border" />
      <button
        type="button"
        aria-pressed={value === ENV_ALL}
        onClick={() => choose(ENV_ALL)}
        data-testid="env-scope-all"
        className={cn(segment, value === ENV_ALL ? "bg-ink/8 font-medium text-ink" : "text-ink-muted hover:text-ink")}
      >
        All
      </button>
      {overflow.length > 0 && (
        <div ref={menuRef} className="relative">
          <span aria-hidden="true" className="mx-0.5 inline-block h-4 w-px bg-border align-middle" />
          <button
            type="button"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            onClick={() => setMenuOpen((open) => !open)}
            data-testid="env-scope-more"
            className={cn(segment, inMenu ? "bg-ink/8 font-medium text-ink" : "text-ink-muted hover:text-ink")}
          >
            {menuLabel}
            <ChevronDown className="h-3 w-3" aria-hidden="true" />
          </button>
          {menuOpen && (
            <div
              role="menu"
              {...escapeLayer}
              className="absolute right-0 top-8 z-[110] max-h-80 min-w-44 overflow-y-auto rounded-lg border border-border bg-surface p-1 shadow-md"
            >
              {overflow.map((option) => (
                <button
                  key={option.name}
                  type="button"
                  role="menuitemradio"
                  aria-checked={value === option.name}
                  onClick={() => choose(option.name)}
                  className={cn(
                    "flex w-full items-center rounded-md px-2.5 py-1.5 text-left text-sm text-ink transition-colors hover:bg-ink/5",
                    value === option.name && "bg-ink/8 font-medium"
                  )}
                >
                  <span className="truncate">{option.name}</span>
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
