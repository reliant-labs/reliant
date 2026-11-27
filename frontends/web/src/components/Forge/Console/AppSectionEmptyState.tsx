// Copyright (c) 2026 Reliant Labs

/**
 * An app page before anything is deployed (U6b, owner 2026-11-20): every
 * project has the same nav, so a project with no deployed app opens the
 * same pages, and each says what it will show and how to get there: a chat
 * that deploys the project, the Move to Forge workflow for an app that lives
 * elsewhere, and the section's guide (EmptyState/ProjectEmptyCta.tsx, U6d).
 */

import { Rocket } from "lucide-react";

import { ProjectEmptyCta } from "@/components/EmptyState/ProjectEmptyCta";

import { learnSectionOf } from "./consoleLearn";
import { consoleNavEntries } from "./consoleNavRegistry";
import { PAGE_SUBTITLES } from "./consoleCrumbs";
import { OBS_NAV_LABELS, type ConsolePage } from "./paths";

/** The page's name and what it shows, in the nav's words. */
function describe(page: ConsolePage): { title: string; shows: string } {
  switch (page.kind) {
    case "overview":
    case "workload":
      return { title: "Overview", shows: PAGE_SUBTITLES.overview };
    case "obs-overview":
      return { title: "Observability", shows: "Requests, errors and latency per service, the top errors, the slowest endpoints and what is alerting" };
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
    case "auth":
      return { title: "Auth", shows: "Who can sign in to your app, and how" };
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
    case "services":
    case "service":
      return { title: "Services", shows: PAGE_SUBTITLES.services };
    case "usage":
      return { title: "Usage", shows: PAGE_SUBTITLES.usage };
    case "events":
      return { title: "Events & metrics", shows: PAGE_SUBTITLES.events };
    case "analytics":
      return { title: "Analytics", shows: PAGE_SUBTITLES.analytics };
    case "site-insights":
      return { title: "Site insights", shows: PAGE_SUBTITLES["site-insights"] };
    case "vulnerabilities":
    case "vulnerability":
      return { title: "Vulnerabilities", shows: "The known vulnerabilities in what your app's releases ship" };
    case "sboms":
      return { title: "SBOMs", shows: "What each release artifact is made of, and its scan" };
    case "tests":
    case "tests-coverage":
    case "tests-runs":
    case "tests-run":
    case "tests-flaky":
    case "tests-flaky-test":
      return { title: "Tests", shows: "Coverage, test runs and flaky tests, per commit, branch and release" };
    case "extension": {
      const label = consoleNavEntries().find((entry) => entry.id === page.id)?.label ?? page.segment;
      return { title: label, shows: `${label} for your app` };
    }
    default:
      // A page kind this list does not name yet (main may add one before this
      // list learns it): a generic description, so a new kind still compiles.
      return { title: "This page", shows: "What your app reports here" };
  }
}

export function AppSectionEmptyState({ page, projectId, title: heading }: { page: ConsolePage; projectId: string | null; title?: string }) {
  const { title, shows } = describe(page);
  return (
    <ProjectEmptyCta
      testId="app-section-empty"
      icon={<Rocket />}
      headingLevel={1}
      title={heading ?? `${title} appears once this project is deployed`}
      projectId={projectId}
      deploy="chat"
      learn={learnSectionOf(page) ?? "overview"}
    >
      <p>{shows}.</p>
      <p>Deploying puts the app on Reliant hosting with prod and staging; this page fills in with its first release. Already have an app somewhere else? Move it onto forge.</p>
    </ProjectEmptyCta>
  );
}
