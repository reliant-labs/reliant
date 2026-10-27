// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the Releases pages. NOT a product surface.
 *
 * The real ReleasesList and ReleaseDetail, inside the real console chrome
 * (ConsolePreviewShell), against the fixture ledger (releaseFixtures.ts) and
 * the observability fixtures for names and DORA — unauthenticated, no
 * backend — so every state can be looked at in every colour scheme and
 * screenshotted. All state is in the URL, so a screenshot is a link:
 *
 *   /forge-releases-preview                                   the list, scoped to prod
 *   /forge-releases-preview?version=20261018.052711-950b560e58ce   a release
 *   …&tab=rollout|checks|artifacts|health   &env=all|e2e|pr-984
 *   …&case=empty|error|loading|ledger-failed|compare-fails|local-env
 *   …&dark=1&scheme=pure-black
 */

import { useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import type { PromotionList } from "@/services/controlPlane/observability/deliveryTypes";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ReleasesApiProvider } from "@/services/forge/ReleasesApiContext";

import { ConsolePreviewShell } from "../../Console/ConsolePreviewShell";
import { withSearch } from "../../Console/paths";
import { createFixtureApi } from "../../Observability/__fixtures__/fixtureApi";
import { ReleaseDetail, type ReleaseDetailSearch } from "../ReleaseDetail";
import { ReleasesList, type PromoteControl, type ReleasesSearch } from "../ReleasesList";
import { shortVersion } from "../releaseModel";
import {
  FIXTURE_DELIVERY,
  FIXTURE_DORA,
  RELEASE_ENVS,
  RELEASE_PROJECT,
  createReleasesFixtureApi,
  type ReleasesFixtureOptions,
} from "../__fixtures__/releaseFixtures";

const BASE = "/forge-releases-preview";

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "purple-classic",
  "pure-black",
] as const;

const CASES: { id: string; label: string; options: ReleasesFixtureOptions }[] = [
  { id: "data", label: "the ledger", options: {} },
  { id: "empty", label: "nothing cut yet", options: { state: "empty" } },
  { id: "loading", label: "loading", options: { state: "loading" } },
  { id: "error", label: "control plane unavailable", options: { state: "error" } },
  { id: "ledger-failed", label: "promotion history unreadable", options: { promotionsFail: true } },
  { id: "compare-fails", label: "CompareReleases fails", options: { compareFails: true } },
  // The scope is a local environment (forge env up): no releases, by design.
  { id: "local-env", label: "scoped to a local env (laptop)", options: {} },
];

type PreviewSearch = ReleasesSearch &
  ReleaseDetailSearch & { version?: string; env?: string; case?: string; dark?: boolean | string | number; scheme?: string };

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

export default function ReleasesPreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const navigate = useNavigate();
  const caseId = search.case ?? "data";
  // The fixture's dev is hosted; the local case scopes to an environment that runs on a laptop.
  const env = search.env ?? (caseId === "local-env" ? "laptop" : "prod");
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  // The router parses `dark=1` as JSON, so it arrives as a number.
  const dark = !!search.dark && String(search.dark) !== "0" && String(search.dark) !== "false";
  const scheme = search.scheme ?? "professional-blue";

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);

  // A fresh client and fixtures per case, so one case's answers never leak into the next.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }), [caseId]);
  const releasesApi = useMemo(() => createReleasesFixtureApi(active.options), [active]);
  const obsApi = useMemo(
    () => ({
      ...createFixtureApi(),
      listReleases: async (req: { window: { startMs: number; endMs: number } }): Promise<PromotionList> => ({
        promotions: FIXTURE_DELIVERY.filter((p) => p.promotedAtMs >= req.window.startMs && p.promotedAtMs <= req.window.endMs),
        truncated: false,
      }),
      getDoraMetrics: async () => FIXTURE_DORA,
    }),
    []
  );

  const go = (next: Partial<PreviewSearch>, replace = true) =>
    void navigate({ to: BASE, search: (prev: Record<string, unknown>) => ({ ...prev, ...next }), replace });
  const href = (extra: Record<string, unknown>) =>
    withSearch(BASE, { env, case: search.case, dark: search.dark, scheme: search.scheme, ...extra });
  const releaseHref = (version: string, extra?: Record<string, unknown>) => href({ version, ...extra });
  const promote: PromoteControl = {
    onPromote: (request) =>
      window.alert(
        `Preview: would open the plan to ${request.intent === "rollback" ? "roll back" : "promote"} ${request.env} ${request.intent === "rollback" ? "to" : "with"} ${shortVersion(request.version)}.`
      ),
    blockedReason: null,
  };

  return (
    <ConsolePreviewShell
      page={search.version ? { kind: "release", version: search.version } : { kind: "releases" }}
      env={env}
      onEnvChange={(next) => go({ env: next })}
      controls={
        <>
          <select data-testid="preview-case" value={caseId} onChange={(e) => go({ case: e.target.value === "data" ? undefined : e.target.value })} className={CONTROL}>
            {CASES.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </select>
          <select data-testid="preview-scheme" value={scheme} onChange={(e) => go({ scheme: e.target.value })} className={CONTROL}>
            {SCHEMES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          <button type="button" data-testid="preview-dark" onClick={() => go({ dark: dark ? undefined : "1" })} className={CONTROL}>
            {dark ? "Dark" : "Light"}
          </button>
        </>
      }
    >
      <QueryClientProvider client={queryClient}>
        <ObservabilityApiProvider api={obsApi as unknown as ReturnType<typeof createFixtureApi>}>
          <ReleasesApiProvider api={releasesApi}>
            {search.version ? (
              <ReleaseDetail
                key={`${caseId}-${search.version}`}
                project={RELEASE_PROJECT}
                version={search.version}
                env={env}
                envs={RELEASE_ENVS}
                envsLoading={false}
                search={{ tab: search.tab, base: search.base, in: search.in }}
                onSearch={(next) => go(next)}
                listHref={href({})}
                releaseHref={releaseHref}
                activityHref={() => href({})}
                promote={promote}
              />
            ) : (
              <ReleasesList
                key={caseId}
                project={RELEASE_PROJECT}
                env={env}
                envs={RELEASE_ENVS}
                envsLoading={false}
                search={{ q: search.q, author: search.author, status: search.status, every: search.every }}
                onSearch={(next) => go(next)}
                releaseHref={releaseHref}
                promote={promote}
                scopeIsLocal={caseId === "local-env" && env === "laptop"}
              />
            )}
          </ReleasesApiProvider>
        </ObservabilityApiProvider>
      </QueryClientProvider>
    </ConsolePreviewShell>
  );
}
