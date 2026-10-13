// Copyright (c) 2025 Reliant Labs

/**
 * Proving harness for the Observability tab. NOT a product surface.
 *
 * Renders the real ObservabilityPanel, inside the real forge shell, against
 * the in-memory fixture API — unauthenticated and with no backend — so every
 * view and state can be looked at in every colour scheme, screenshotted, and
 * driven by the Playwright smoke (e2e/observability.spec.ts). Dev builds only:
 * the route sits behind DevOnlyRoute in routes.tsx.
 *
 * The URL state the env page keeps in search params is held in component
 * state here; the panel cannot tell the difference.
 */

import { useMemo, useState } from "react";
import { Code } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { ForgeShell } from "../../ForgeShell";
import ObservabilityPanel from "../ObservabilityPanel";
import type { ObsNavigate, ObsSearch } from "../obsSearch";
import { createFixtureApi, type FixtureOptions } from "../__fixtures__/fixtureApi";

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "forest",
  "vibrant-pink",
  "energetic-orange",
  "bold-red",
  "purple-classic",
  "pure-black",
] as const;

const CASES: { id: string; label: string; options: FixtureOptions }[] = [
  { id: "data", label: "telemetry flowing", options: {} },
  { id: "not-set-up", label: "org not set up (FailedPrecondition)", options: { failWith: Code.FailedPrecondition } },
  { id: "empty", label: "no data in range", options: { empty: true } },
  { id: "unavailable", label: "service unavailable", options: { failWith: Code.Unavailable } },
  { id: "quota", label: "query quota hit", options: { failWith: Code.ResourceExhausted } },
  // Usage states (view=usage): the allowance ladder as an org on each rung sees it.
  { id: "usage-near", label: "usage: 85% of allowance", options: { usage: { usedBytes: 8.5 * 2 ** 30 } } },
  { id: "usage-over", label: "usage: over, unfunded (dropping)", options: { usage: { usedBytes: 11 * 2 ** 30, state: "over_100", usageState: "over_100" } } },
  { id: "usage-dropping", label: "usage: over 150% (most dropped)", options: { usage: { usedBytes: 16 * 2 ** 30, state: "over_150", usageState: "over_150" } } },
  { id: "usage-billed", label: "usage: over, funded (billed)", options: { usage: { usedBytes: 14 * 2 ** 30, state: "ok", usageState: "over_100", funded: true, overageCents: 100, projectedOverageCents: 180 } } },
  { id: "usage-cap", label: "usage: spend cap reached", options: { usage: { usedBytes: 230 * 2 ** 30, state: "over_100", usageState: "over_150", funded: true, spendCapReached: true, overageCents: 5000 } } },
  { id: "usage-exempt", label: "usage: exempt org", options: { usage: { exempt: true, usedBytes: 40 * 2 ** 30, usageState: "over_150" } } },
];

export default function ObservabilityPreview() {
  const initial = new URLSearchParams(window.location.search);
  const [scheme, setScheme] = useState(() => document.documentElement.getAttribute("data-color-scheme") ?? "professional-blue");
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  const [caseId, setCaseId] = useState(() => initial.get("case") ?? CASES[0].id);
  const [search, setSearch] = useState<ObsSearch>(() => ({ view: (initial.get("view") as ObsSearch["view"]) ?? undefined }));

  const active = CASES.find((c) => c.id === caseId) ?? CASES[0];
  // A fresh client per case, so one case's cached answers never leak into the next.
  const queryClient = useMemo(
    () => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [caseId]
  );
  const api = useMemo(() => createFixtureApi(active.options), [active]);

  const navigate: ObsNavigate = (next) => {
    setSearch((prev) => {
      const merged: Record<string, unknown> = { ...prev, ...next };
      for (const key of Object.keys(merged)) if (merged[key] === undefined) delete merged[key];
      return merged as ObsSearch;
    });
  };

  function applyScheme(next: string) {
    setScheme(next);
    document.documentElement.setAttribute("data-color-scheme", next);
  }

  function applyDark(next: boolean) {
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
  }

  return (
    <ForgeShell
      activePath="/forge/env/prod"
      envs={[
        { name: "dev", lifecycle: "local", source: "backend" },
        { name: "prod", lifecycle: "deployed", source: "backend" },
      ]}
      headerContent={
        <div className="flex flex-wrap items-center gap-3 px-4">
          <select
            data-testid="preview-case"
            value={caseId}
            onChange={(e) => setCaseId(e.target.value)}
            className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
          >
            {CASES.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </select>
          <select
            data-testid="preview-scheme"
            value={scheme}
            onChange={(e) => applyScheme(e.target.value)}
            className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
          >
            {SCHEMES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          <button
            type="button"
            data-testid="preview-dark"
            onClick={() => applyDark(!dark)}
            className="rounded-md border border-border bg-card px-3 py-1 text-sm text-foreground"
          >
            {dark ? "Dark" : "Light"}
          </button>
        </div>
      }
    >
      <div className="h-full overflow-y-auto p-6">
        <QueryClientProvider client={queryClient}>
          <ObservabilityApiProvider api={api}>
            <ObservabilityPanel key={caseId} environmentId="env-prod" envName="prod" search={search} navigate={navigate} />
          </ObservabilityApiProvider>
        </QueryClientProvider>
      </div>
    </ForgeShell>
  );
}
