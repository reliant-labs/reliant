// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the Releases pages. NOT a product surface.
 *
 * The real console's Releases list and a release (ReleasesPage,
 * ReleasePage), in the real console, on their real clients over the shop
 * fixture pack (ConsoleFixturePreview) — unauthenticated, no backend — so
 * every state can be looked at in every colour scheme and screenshotted. A
 * case is a named overlay over the pack (src/fixtures/shop/overlays.ts
 * deliveryOverlays.releases). The harness's URL says where it opens:
 *
 *   /forge-releases-preview                                   the list, scoped to prod
 *   /forge-releases-preview?version=v1.8.3                    a release
 *   …&tab=rollout|checks|artifacts|health   &env=all|staging|dev
 *   …&case=empty|error|loading|ledger-failed|compare-fails
 *   …&dark=1&scheme=pure-black
 */

import { useSearch } from "@tanstack/react-router";

import { DEMO_PROJECT } from "@/fixtures/shop/handlers/scope";
import { deliveryOverlays } from "@/fixtures/shop/overlays";

import { consolePath, withSearch } from "../../Console/paths";
import { ConsoleFixturePreview, isDark, PreviewControls } from "../../__preview__/ConsoleFixturePreview";

const BASE = "/forge-releases-preview";

const overlays = deliveryOverlays.releases;

const CASES = [
  { id: "data", label: "the ledger" },
  { id: "empty", label: "nothing cut yet", overlay: overlays.empty },
  { id: "loading", label: "loading", overlay: overlays.loading },
  { id: "error", label: "control plane unavailable", overlay: overlays.error },
  { id: "ledger-failed", label: "promotion history unreadable", overlay: overlays["ledger-failed"] },
  { id: "compare-fails", label: "CompareReleases fails", overlay: overlays["compare-fails"] },
] as const;

type PreviewSearch = { version?: string; env?: string; tab?: string; base?: string; in?: string; case?: string; dark?: boolean | string | number; scheme?: string };

export default function ReleasesPreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const caseId = search.case ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0];
  const page = search.version ? ({ kind: "release", version: search.version } as const) : ({ kind: "releases" } as const);
  const path = withSearch(consolePath(DEMO_PROJECT, page), { env: search.env ?? "prod", tab: search.tab, base: search.base, in: search.in });
  return (
    <ConsoleFixturePreview
      key={`${caseId}|${path}`}
      path={path}
      overlay={"overlay" in active ? active.overlay : null}
      controls={<PreviewControls base={BASE} cases={CASES} caseId={active.id} defaultCase="data" scheme={search.scheme ?? "professional-blue"} dark={isDark(search.dark)} />}
    />
  );
}
