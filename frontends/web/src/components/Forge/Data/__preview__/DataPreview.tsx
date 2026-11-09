// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the data browser. NOT a product surface.
 *
 * The real DataBrowser, inside the real console chrome (ConsolePreviewShell),
 * against the fixture shop (fixtureDataApi.ts) — unauthenticated, no backend
 * — so every state can be looked at in every colour scheme and screenshotted.
 * The browser's state is this page's own URL, as it is the console's, so a
 * screenshot is a link:
 *
 *   /forge-data-preview                                  staging (open), the default database
 *   …?env=prod                                           prod: audited, asks for a reason
 *   …&table=public.orders&f=%5B%22status.eq.paid%22%5D   a filtered grid
 *   …&table=public.orders&row=%221004%22                 a row and its Related
 *   …&case=deny|denied|empty|unreachable|slow|many-refs
 *   …&dark=1&scheme=pure-black
 */

import { useCallback, useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Code } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import PageHeader from "@/components/forge-ui/page_header";
import { DataBrowserApiProvider } from "@/services/controlPlane/observability/DataBrowserApiContext";

import { ConsolePreviewShell } from "../../Console/ConsolePreviewShell";
import { withSearch } from "../../Console/paths";
import { createDataFixtureApi, DATA_FIXTURE_ENVS, type DataFixtureOptions } from "../__fixtures__/fixtureDataApi";
import { DataBrowser } from "../DataBrowser";
import { DATA_SEARCH_KEYS, type DataSearch } from "../dataUrl-model";

const BASE = "/forge-data-preview";

const SCHEMES = ["reliant", "professional-blue", "refined-neutral", "modern-teal", "slate", "purple-classic", "pure-black"] as const;

const CASES: { id: string; label: string; options: DataFixtureOptions }[] = [
  { id: "data", label: "the shop (prod audited, staging open)", options: {} },
  { id: "deny", label: "policy: deny", options: { policy: { "env-prod": "deny", "env-staging": "deny", "env-dev": "deny" } } },
  { id: "denied", label: "no db:query grant (PermissionDenied)", options: { fail: { method: "listBrowsableDatabases", code: Code.PermissionDenied } } },
  { id: "empty", label: "no databases declared", options: { noDatabases: true } },
  { id: "unreachable", label: "describe: datasource unreachable", options: { fail: { method: "describeDatabase", code: Code.FailedPrecondition, reason: "datasource_unreachable" } } },
  { id: "timeout", label: "browse: statement timeout", options: { fail: { method: "browseTable", code: Code.DeadlineExceeded } } },
  { id: "slow", label: "slow answers (loading states)", options: { delayMs: 1500 } },
  { id: "many-refs", label: "a user referenced by 14 tables", options: { extraReferences: 12, delayMs: 300 } },
];

type PreviewSearch = DataSearch & { env?: string; case?: string; dark?: boolean | string | number; scheme?: string };

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

export default function DataPreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const navigate = useNavigate();
  const caseId = search.case ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  const envName = search.env ?? "staging";
  const env = DATA_FIXTURE_ENVS.find((candidate) => candidate.name === envName) ?? DATA_FIXTURE_ENVS[1];
  const dark = !!search.dark && String(search.dark) !== "0" && String(search.dark) !== "false";
  const scheme = search.scheme ?? "reliant";

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);

  // A fresh client and fixture per case (and environment), so answers never leak between them.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }), [caseId, env.id]);
  const api = useMemo(() => createDataFixtureApi(active.options), [active]);

  const own = { env: search.env, case: search.case, dark: search.dark, scheme: search.scheme };
  const data = useMemo(() => {
    const out: Record<string, unknown> = {};
    for (const key of DATA_SEARCH_KEYS) if (search[key] !== undefined) out[key] = search[key];
    return out as DataSearch;
  }, [search]);
  const go = (next: Record<string, unknown>) => void navigate({ to: BASE, search: (prev: Record<string, unknown>) => ({ ...prev, ...next }), replace: true });
  const onNavigate = useCallback(
    (next: DataSearch, options?: { replace?: boolean }) =>
      void navigate({ to: BASE, search: { ...own, ...next } as Record<string, unknown>, replace: options?.replace ?? false }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [navigate, search.env, search.case, search.dark, search.scheme]
  );
  const href = (next: DataSearch) => withSearch(BASE, { ...own, ...next });

  return (
    <ConsolePreviewShell
      page={{ kind: "data" }}
      project="shop"
      envs={DATA_FIXTURE_ENVS.map((candidate) => ({ name: candidate.name, preview: false }))}
      env={env.name}
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
          <button type="button" onClick={() => window.sessionStorage.clear()} className={CONTROL}>
            Forget reasons
          </button>
        </>
      }
    >
      <div className="space-y-4">
        <PageHeader density="compact" title="Data" className="mb-0" />
        <QueryClientProvider client={queryClient}>
          <DataBrowserApiProvider api={api}>
            <DataBrowser key={`${caseId}-${env.id}`} env={env} search={data} onNavigate={onNavigate} href={href} />
          </DataBrowserApiProvider>
        </QueryClientProvider>
      </div>
    </ConsolePreviewShell>
  );
}
