// Copyright (c) 2025 Reliant Labs

/**
 * The app switcher, in the sidebar's brand row: the one scope that changes the
 * nav. It names the FORGE app the console is reporting on — the
 * URL's subject — and lists the org's apps (the Apps list's, useApps), with
 * "All apps" on top leading back to that list (/apps).
 *
 * The URL's app is always listed and checked, even when the control plane has
 * not listed it (a Reliant-only or local project opened by URL).
 *
 * The list is the project selector (Projects/ProjectSelector, app mode): one
 * row per app, each with the project whose code holds it, searchable and
 * pinned like every other picker. The control plane's list (useApps) is
 * passed in too, so an app the catalog cannot see (the forge shell has no
 * Reliant projects) is still listed.
 *
 * Choosing an app keeps the reader on the same page of that app
 * (ConsoleLayout.switchTarget). It does not select a Reliant project in the
 * store itself: the console layout selects whichever project declares the app
 * it lands on, so the store follows the URL rather than the other way round.
 */

import { ChevronsUpDown, LayoutGrid } from "lucide-react";

import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { useApps } from "@/hooks/apps-queries";
import { ProjectSelector } from "@/components/Projects/ProjectSelector";

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

const ALL_APPS_KEY = "all-apps";

export function ConsoleProjectSwitcher({ forgeProject, onSelect, onSelectAll }: ConsoleProjectSwitcherProps) {
  const orgApps = useApps();
  // The URL's app is always listed, even when the control plane has not listed it.
  const listed = orgApps.apps.map((app) => app.name);
  const names = listed.includes(forgeProject) ? listed : [...listed, forgeProject];
  // A build with no control plane lists only this checkout's apps; nothing is "unavailable" about that.
  const unavailable =
    !orgApps.loading && orgApps.availability !== "available" && orgApps.availability !== "no-control-plane";
  const { noDragRegionStyle } = useTitleBarChrome({ alignedToWindowEdge: false });

  return (
    <div className="relative min-w-0 flex-1" style={noDragRegionStyle}>
      <ProjectSelector
        mode="app"
        align="left"
        selectedKey={forgeProject}
        extraApps={names}
        notice={unavailable ? "Other apps are unavailable right now." : undefined}
        leadItems={[
          {
            key: ALL_APPS_KEY,
            label: "All apps",
            icon: <LayoutGrid className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />,
            onSelect: onSelectAll,
            testId: "forge-project-menu-all-apps",
          },
        ]}
        onSelect={(selection) => {
          if (selection.kind === "app") onSelect(selection.app.name);
        }}
        trigger={(props) => (
          <button
            type="button"
            {...props}
            data-testid="forge-project-switcher"
            aria-label={`App: ${forgeProject}. Switch app`}
            className="flex h-8 w-full min-w-0 items-center gap-2 rounded-md px-1.5 text-left text-sm font-semibold text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <ProjectMonogram name={forgeProject} />
            <span className="min-w-0 flex-1 truncate">{forgeProject}</span>
            <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />
          </button>
        )}
      />
    </div>
  );
}
