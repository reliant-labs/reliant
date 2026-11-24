// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the Overview and the workload page. NOT a
 * product surface.
 *
 * The real console's Overview and a workload (OverviewPage, WorkloadPage),
 * in the real console, on their real clients over the shop fixture pack
 * (ConsoleFixturePreview) — unauthenticated, no backend — so every state can
 * be looked at in every colour scheme and screenshotted. A case is a named
 * overlay over the pack (src/fixtures/shop/overlays.ts
 * deliveryOverlays.workloads). The harness's URL says where it opens:
 *
 *   /forge-workloads-preview                                   the Overview, scoped to prod
 *   /forge-workloads-preview?env=all                           every environment
 *   /forge-workloads-preview?workload=worker                   a workload (in the scoped env)
 *   …&tab=logs|traces|profiles|events|history|config
 *   …&case=crash-looping|empty|loading|error|not-set-up|self-managed|local|no-default-branch|action-fails
 *   …&case=config-denied|config-unsupported|no-envs             the Config tab's and the roster's other states
 *   …&case=no-frontends|frontends-loading|domains-unreadable      the Frontends panel's other states
 *   …&dark=1&scheme=pure-black
 *
 * The repository's default branch (Scale's "Open PR") is GitHub's answer, not
 * a Connect read, so the harness answers it itself: `main`, or unknown.
 */

import type { ReactNode } from "react";
import { useSearch } from "@tanstack/react-router";

import { DEMO_PROJECT } from "@/fixtures/shop/handlers/scope";
import { deliveryOverlays } from "@/fixtures/shop/overlays";
import { RepoMetaProvider, type RepoMetaApi } from "@/services/forge/RepoMetaContext";

import { consolePath, withSearch } from "../../Console/paths";
import { ConsoleFixturePreview, isDark, PreviewControls } from "../../__preview__/ConsoleFixturePreview";

const BASE = "/forge-workloads-preview";

const overlays = deliveryOverlays.workloads;

const CASES = [
  { id: "data", label: "the shop" },
  { id: "crash-looping", label: "the worker crash-looping", overlay: overlays["crash-looping"] },
  { id: "empty", label: "nothing reported", overlay: overlays.empty },
  { id: "loading", label: "loading", overlay: overlays.loading },
  { id: "error", label: "telemetry unavailable", overlay: overlays.error },
  { id: "not-set-up", label: "observability not set up", overlay: overlays["not-set-up"] },
  { id: "self-managed", label: "self-managed prod (no restart)", overlay: overlays["self-managed"] },
  { id: "local", label: "local prod (no restart)", overlay: overlays.local },
  { id: "no-default-branch", label: "repo default branch unknown" },
  { id: "action-fails", label: "restart / stop refused", overlay: overlays["action-fails"] },
  { id: "config-denied", label: "config grant refused (Config tab)", overlay: overlays["config-denied"] },
  { id: "config-unsupported", label: "config unsupported (Config tab)", overlay: overlays["config-unsupported"] },
  { id: "no-envs", label: "nothing deployed", overlay: overlays["no-envs"] },
  { id: "no-frontends", label: "no frontend deployed", overlay: overlays["no-frontends"] },
  { id: "frontends-loading", label: "frontends loading", overlay: overlays["frontends-loading"] },
  { id: "domains-unreadable", label: "domains unreadable (no custom-domain column)", overlay: overlays["domains-unreadable"] },
] as const;

const MAIN: RepoMetaApi = { defaultBranch: async () => "main" };
const UNKNOWN: RepoMetaApi = { defaultBranch: async () => null };

type PreviewSearch = { workload?: string; env?: string; tab?: string; case?: string; dark?: boolean | string | number; scheme?: string } & Record<string, unknown>;

/** The workload page's own search keys, passed through to the console. */
const DETAIL_KEYS = ["tab", "range", "q", "severity", "trace", "span", "profileType", "status", "kind"] as const;

export default function WorkloadsPreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const caseId = search.case ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0];
  const env = search.env ?? "prod";
  const page = search.workload ? ({ kind: "workload", env: env === "all" ? "prod" : env, name: search.workload } as const) : ({ kind: "overview" } as const);
  const passed = Object.fromEntries(DETAIL_KEYS.map((key) => [key, search[key]]));
  const path = withSearch(consolePath(DEMO_PROJECT, page), { env, ...passed });
  const repoMeta = active.id === "no-default-branch" ? UNKNOWN : MAIN;
  return (
    <ConsoleFixturePreview
      key={`${caseId}|${path}`}
      path={path}
      overlay={"overlay" in active ? active.overlay : null}
      wrap={(console: ReactNode) => <RepoMetaProvider api={repoMeta}>{console}</RepoMetaProvider>}
      controls={<PreviewControls base={BASE} cases={CASES} caseId={active.id} defaultCase="data" scheme={search.scheme ?? "professional-blue"} dark={isDark(search.dark)} />}
    />
  );
}
