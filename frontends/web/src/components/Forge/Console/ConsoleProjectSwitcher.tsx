// Copyright (c) 2025 Reliant Labs

/**
 * The app switcher, in the sidebar's brand row: the one scope that changes the
 * nav (PROPOSAL §3.1). It names the FORGE app the console is reporting on — the
 * URL's subject — and lists the org's apps (the Apps list's, useApps), with
 * "All apps" on top leading back to that list (/apps).
 *
 * The URL's app is always listed and checked, even when the control plane has
 * not listed it (a Reliant-only or local project opened by URL).
 *
 * Choosing an app keeps the reader on the same page of that app
 * (ConsoleLayout.switchTarget). It does not select a Reliant project in the
 * store itself: the console layout selects whichever project declares the app
 * it lands on, so the store follows the URL rather than the other way round.
 */

import { useEffect, useRef, useState } from "react";
import { Check, ChevronsUpDown, LayoutGrid } from "lucide-react";

import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { cn } from "@/lib/utils";
import { useApps } from "@/hooks/apps-queries";

/** The project's monogram — the brand row on the collapsed rail. */
export function ProjectMonogram({ name }: { name: string }) {
  return (
    <span
      aria-hidden="true"
      className="grid h-5.5 w-5.5 shrink-0 place-items-center rounded-md bg-accent text-xs font-semibold text-on-accent"
    >
      {(name.charAt(0) || "?").toUpperCase()}
    </span>
  );
}

export interface ConsoleProjectSwitcherProps {
  forgeProject: string;
  /** Switch to the forge app named `app`. */
  onSelect: (app: string) => void;
  /** Leave the app for the org's Apps list. */
  onSelectAll: () => void;
}

export function ConsoleProjectSwitcher({ forgeProject, onSelect, onSelectAll }: ConsoleProjectSwitcherProps) {
  const orgApps = useApps();
  // A handful of names, rebuilt per render like the list they come from (useApps builds it per render).
  const listed = orgApps.apps.map((app) => app.name);
  const names = (listed.includes(forgeProject) ? listed : [...listed, forgeProject]).sort((a, b) => a.localeCompare(b));
  // A build with no control plane lists only this checkout's apps; nothing is "unavailable" about that.
  const unavailable =
    !orgApps.loading && orgApps.availability !== "available" && orgApps.availability !== "no-control-plane";
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);
  const escapeLayer = useEscapeLayer(open, () => setOpen(false));
  const { noDragRegionStyle } = useTitleBarChrome({ alignedToWindowEdge: false });

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [open]);

  return (
    <div ref={ref} className="relative min-w-0 flex-1" style={noDragRegionStyle}>
      <button
        type="button"
        data-testid="forge-project-switcher"
        onClick={() => setOpen((prev) => !prev)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`App: ${forgeProject}. Switch app`}
        className="flex h-8 w-full min-w-0 items-center gap-2 rounded-md px-1.5 text-left text-sm font-semibold text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        <ProjectMonogram name={forgeProject} />
        <span className="min-w-0 flex-1 truncate">{forgeProject}</span>
        <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />
      </button>

      {open && (
        <div
          role="menu"
          {...escapeLayer}
          data-testid="forge-project-menu"
          className="absolute left-0 top-10 z-[110] max-h-80 w-64 overflow-y-auto rounded-lg border border-border bg-surface p-1 shadow-md"
        >
          <button
            type="button"
            role="menuitem"
            data-testid="forge-project-menu-all-apps"
            onClick={() => {
              setOpen(false);
              onSelectAll();
            }}
            className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm text-ink transition-colors hover:bg-ink/5"
          >
            <LayoutGrid className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />
            <span className="min-w-0 flex-1 truncate">All apps</span>
          </button>
          <div role="separator" className="my-1 h-px bg-border" />
          {names.map((name) => (
            <button
              key={name}
              type="button"
              role="menuitem"
              data-testid={`forge-project-menu-item-${name}`}
              onClick={() => {
                setOpen(false);
                onSelect(name);
              }}
              className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm text-ink transition-colors hover:bg-ink/5"
            >
              <Check className={cn("h-3.5 w-3.5 shrink-0", name !== forgeProject && "invisible")} aria-hidden="true" />
              <span className="min-w-0 flex-1 truncate">{name}</span>
            </button>
          ))}
          {orgApps.loading && <p className="px-3 py-2 text-sm text-ink-muted">Loading apps…</p>}
          {unavailable && <p className="px-3 py-2 text-sm text-ink-muted">Other apps are unavailable right now.</p>}
        </div>
      )}
    </div>
  );
}
