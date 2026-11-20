// Copyright (c) 2026 Reliant Labs

/**
 * An app page before anything is deployed (U6b, owner 2026-11-20): every
 * project has the same nav, so a project with no deployed app opens the
 * same pages, and each says what it will show and how to get there, with ONE
 * action — a chat that deploys the project.
 */

import { useNavigate } from "@tanstack/react-router";
import { Rocket } from "lucide-react";

import { buttonClasses } from "@/components/forge-ui/button";
import { homePath, projectChatsPath, withSearch } from "@/lib/paths";
import { cn } from "@/lib/utils";

import { START_APP, START_REPO } from "../../Apps/appStart";
import { consoleNavEntries } from "./consoleNavRegistry";
import { PAGE_SUBTITLES } from "./groupedNav";
import { OBS_NAV_LABELS, type ConsolePage } from "./paths";

/** The page's name and what it shows, in the nav's words. */
function describe(page: ConsolePage): { title: string; shows: string } {
  switch (page.kind) {
    case "overview":
    case "workload":
      return { title: "Overview", shows: PAGE_SUBTITLES.overview };
    case "obs-view":
      return { title: OBS_NAV_LABELS[page.view], shows: PAGE_SUBTITLES[page.view] ?? "" };
    case "dashboard":
      return { title: "Dashboards", shows: PAGE_SUBTITLES.dashboards };
    case "releases":
    case "release":
      return { title: "Releases", shows: PAGE_SUBTITLES.releases };
    case "data":
      return { title: "Data", shows: PAGE_SUBTITLES.data };
    case "users":
      return { title: "Users", shows: "Who can sign in to your app, and when they last did" };
    case "storage":
      return { title: "Storage", shows: "The files your app keeps in its buckets" };
    case "environments":
    case "environment":
      return { title: "Environments", shows: "Where your app runs: prod, staging and every preview" };
    case "secrets":
      return { title: "Secrets", shows: "The keys and passwords your app reads, per environment" };
    case "domains":
      return { title: "Domains", shows: "The addresses your app answers on" };
    case "activity":
      return { title: "Activity", shows: "Every deploy, change and alert, newest first" };
    case "extension": {
      const label = consoleNavEntries().find((entry) => entry.id === page.id)?.label ?? page.segment;
      return { title: label, shows: `${label} for your app` };
    }
  }
}

export function AppSectionEmptyState({ page, projectId }: { page: ConsolePage; projectId: string | null }) {
  const navigate = useNavigate();
  const { title, shows } = describe(page);
  // A chat that deploys this project's code; with no code here, one that builds an app.
  const href = projectId ? withSearch(projectChatsPath(projectId), { start: START_REPO }) : withSearch(homePath(), { start: START_APP });
  return (
    <div className="mx-auto flex max-w-[var(--form-max)] flex-col items-start gap-3 py-16" data-testid="app-section-empty">
      <span className="flex size-9 items-center justify-center rounded-xl border border-border text-ink-muted">
        <Rocket className="size-4" aria-hidden="true" />
      </span>
      <div>
        <h1 className="text-section text-ink">{title} appears once this project is deployed</h1>
        <p className="mt-1 text-ui text-ink-muted">{shows}.</p>
        <p className="mt-1 text-ui text-ink-muted">
          Deploying puts the app on Reliant hosting with prod and staging; this page fills in with its first release.
        </p>
      </div>
      <a
        href={href}
        onClick={(event) => {
          if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
          event.preventDefault();
          void navigate({ href });
        }}
        className={cn("no-color", buttonClasses({ variant: "primary", size: "sm" }))} data-testid="app-section-empty-deploy">
        Deploy this project
      </a>
    </div>
  );
}
