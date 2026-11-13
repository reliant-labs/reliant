// Copyright (c) 2026 Reliant Labs

/**
 * What the tidied console (the "75" of the demo density scale) draws under a
 * page's title, through ConsoleNavExtras.pageLead: the page's one "Put an
 * agent on it" line on Errors, Alerts, Traces and Releases, and on Releases
 * the folded "How releases work" panel above it. Every other page: nothing.
 */

import type { ReactNode } from "react";
import { useRouterState } from "@tanstack/react-router";

import type { ConsolePage } from "../../Console/paths";
import { PageAgentLine } from "./AgentCards";
import { HowReleasesWork } from "./HowReleasesWork";

/** A list page, not one item opened in it: a detail page has its own "Ask an agent". */
function OnListOnly({ children }: { children: ReactNode }) {
  const search = useRouterState({ select: (s) => s.location.search }) as Record<string, unknown>;
  const opened = ["issue", "trace", "session", "rule"].some((key) => typeof search[key] === "string" && search[key]);
  return opened ? null : <>{children}</>;
}

export function densityPageLead(page: ConsolePage): ReactNode | null {
  const lead = leadFor(page);
  return lead ? <OnListOnly>{lead}</OnListOnly> : null;
}

function leadFor(page: ConsolePage): ReactNode | null {
  if (page.kind === "obs-view" && page.view === "errors") {
    return <PageAgentLine id="fix-errors" lead="when a new issue appears, an agent reads the stack, the trace and the change behind it, and drafts the fix." />;
  }
  if (page.kind === "obs-view" && page.view === "alerts") {
    return <PageAgentLine id="watch-alerts" lead="when an alert fires, an agent looks first and has an issue, or a fix, waiting." />;
  }
  if (page.kind === "obs-view" && page.view === "traces") {
    return <PageAgentLine id="slow-pages" lead="hand an agent a slow trace and it finds where the time goes." />;
  }
  if (page.kind === "releases") {
    return (
      <div className="space-y-3">
        <HowReleasesWork />
        <PageAgentLine id="release-guard" lead="check every promotion against the hour before, and roll back to the last good release if it regressed." />
      </div>
    );
  }
  return null;
}
