// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the storage browser. NOT a product surface.
 *
 * The real StorageBrowser, inside the real console chrome
 * (ConsolePreviewShell), against the fixture buckets (fixtureStorageApi.ts)
 * — unauthenticated, no backend — so every state can be looked at in every
 * colour scheme and screenshotted. The browser's state is this page's own
 * URL, as it is the console's, so a screenshot is a link:
 *
 *   /forge-storage-preview                               staging (open), the uploads bucket
 *   …?env=prod                                           prod: audited, asks for a reason
 *   …&prefix=avatars%2F2026%2F&object=avatars%2F2026%2Fada.png   an image preview
 *   …&object=docs%2Fconfig.json                          a JSON preview
 *   …&bucket=exports&prefix=daily%2F                     three pages
 *   …&case=deny|denied|empty|shape|slow|rate
 *   …&dark=1&scheme=pure-black
 *
 * Download does not leave the page here: it shows that a URL was minted (but
 * never the URL) in the strip above.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Code } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import PageHeader from "@/components/forge-ui/page_header";
import { StorageBrowserApiProvider } from "@/services/controlPlane/storageBrowser/StorageBrowserApiContext";

import { ConsolePreviewShell } from "../../Console/ConsolePreviewShell";
import { withSearch } from "../../Console/paths";
import { createStorageFixtureApi, STORAGE_FIXTURE_ENVS, type StorageFixtureOptions } from "../__fixtures__/fixtureStorageApi";
import { StorageBrowser } from "../StorageBrowser";
import { pickStorageSearch, type StorageSearch } from "../storageUrl-model";

const BASE = "/forge-storage-preview";

const SCHEMES = ["reliant", "professional-blue", "refined-neutral", "modern-teal", "slate", "purple-classic", "pure-black"] as const;

const CASES: { id: string; label: string; options: StorageFixtureOptions }[] = [
  { id: "storage", label: "the buckets (prod audited, staging open)", options: {} },
  { id: "deny", label: "policy: deny", options: { policy: { "env-prod": "deny", "env-staging": "deny", "env-dev": "deny" } } },
  { id: "denied", label: "no storage:read grant (PermissionDenied)", options: { fail: { method: "listBrowsableBuckets", code: Code.PermissionDenied } } },
  { id: "empty", label: "no buckets declared", options: { noBuckets: true } },
  { id: "shape", label: "no recorded release (shape_unknown)", options: { fail: { method: "listBrowsableBuckets", code: Code.FailedPrecondition, reason: "shape_unknown" } } },
  { id: "rate", label: "listing: rate limited", options: { fail: { method: "listObjects", code: Code.ResourceExhausted, reason: "rate_limited" } } },
  { id: "slow", label: "slow answers (loading states)", options: { delayMs: 1500 } },
];

type PreviewSearch = StorageSearch & { env?: string; case?: string; dark?: boolean | string | number; scheme?: string };

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

export default function StoragePreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const navigate = useNavigate();
  const caseId = search.case ?? "storage";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  const envName = search.env ?? "staging";
  const env = STORAGE_FIXTURE_ENVS.find((candidate) => candidate.name === envName) ?? STORAGE_FIXTURE_ENVS[1];
  const dark = !!search.dark && String(search.dark) !== "0" && String(search.dark) !== "false";
  const scheme = search.scheme ?? "reliant";
  const [minted, setMinted] = useState(0);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);

  // A fresh client and fixture per case (and environment), so answers never leak between them.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }), [caseId, env.id]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const api = useMemo(() => createStorageFixtureApi(active.options), [caseId, env.id]);

  const own = useMemo(() => {
    const out: Record<string, unknown> = {};
    for (const key of ["env", "case", "dark", "scheme"] as const) if (search[key] !== undefined) out[key] = search[key];
    return out;
  }, [search]);
  const storage = useMemo(() => pickStorageSearch(search as Record<string, unknown>), [search]);
  const go = (next: Record<string, unknown>) => void navigate({ to: BASE, search: (prev: Record<string, unknown>) => ({ ...prev, ...next }), replace: true });
  const onNavigate = useCallback(
    (next: StorageSearch, options?: { replace?: boolean }) =>
      void navigate({ to: BASE, search: { ...own, ...next } as Record<string, unknown>, replace: options?.replace ?? false }),
    [navigate, own]
  );
  const href = (next: StorageSearch) => withSearch(BASE, { ...own, ...next });

  return (
    <ConsolePreviewShell
      page={{ kind: "storage" }}
      project="shop"
      envs={STORAGE_FIXTURE_ENVS.map((candidate) => ({ name: candidate.name, preview: false }))}
      env={env.name}
      onEnvChange={(next) => go({ env: next })}
      controls={
        <>
          <select data-testid="preview-case" value={caseId} onChange={(e) => go({ case: e.target.value === "storage" ? undefined : e.target.value })} className={CONTROL}>
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
            Forget reasons and opens
          </button>
          {minted > 0 && <span className="text-xs text-ink-muted">{minted} download URL{minted === 1 ? "" : "s"} minted</span>}
        </>
      }
    >
      <div className="space-y-4">
        <PageHeader density="compact" title="Storage" className="mb-0" />
        <QueryClientProvider client={queryClient}>
          <StorageBrowserApiProvider api={api}>
            <StorageBrowser
              key={`${caseId}-${env.id}`}
              env={env}
              search={storage}
              onNavigate={onNavigate}
              href={href}
              onDownloadUrl={() => setMinted((count) => count + 1)}
            />
          </StorageBrowserApiProvider>
        </QueryClientProvider>
      </div>
    </ConsolePreviewShell>
  );
}
