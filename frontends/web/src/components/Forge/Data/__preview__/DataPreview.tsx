// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the data browser. NOT a product surface.
 *
 * The real DataBrowser, inside the real console chrome (ConsolePreviewShell),
 * on its real client over the shop fixture pack (src/fixtures/shop,
 * <FixturePreview>) — unauthenticated, no backend — so every state can be
 * looked at in every colour scheme and screenshotted. A case is a named
 * overlay over the pack (src/fixtures/shop/overlays.ts browserOverlays.data).
 * The browser's state is this page's own URL, as it is the console's, so a
 * screenshot is a link:
 *
 *   /forge-data-preview                                  staging (open), the default database
 *   …?env=prod                                           prod: audited, asks for a reason
 *   …&table=public.orders&f=%5B%22state.eq.paid%22%5D    a filtered grid
 *   …&table=public.customers&row=1                       a row and its Related
 *   …&case=deny|denied|empty|unreachable|slow|many-refs
 *   …&dark=1&scheme=pure-black
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import PageHeader from "@/components/forge-ui/page_header";
import { FixturePreview, type FixtureServeOptions } from "@/fixtures/preview";
import { latency } from "@/fixtures/overlay";
import { SHOP_SERVE, shopPack } from "@/fixtures/shop";
import { DEMO_ENVS } from "@/fixtures/shop/handlers/scope";
import { browserOverlays } from "@/fixtures/shop/overlays";
import type { FixturePack } from "@/fixtures/transport";

import { createFixtureReader, FixtureReaderProvider } from "../../AuditedAccess/readerSession";
import { ConsolePreviewShell } from "../../Console/ConsolePreviewShell";
import { withSearch } from "../../Console/paths";
import { DataBrowser } from "../DataBrowser";
import { DATA_SEARCH_KEYS, type DataSearch } from "../dataUrl-model";

const BASE = "/forge-data-preview";

const SCHEMES = ["reliant", "professional-blue", "refined-neutral", "modern-teal", "slate", "purple-classic", "pure-black"] as const;

const SHOP = shopPack();
const overlays = browserOverlays.data;
const slow = (ms: number): FixtureServeOptions => ({ ...SHOP_SERVE, interceptors: [latency(ms)] });

const CASES: { id: string; label: string; overlay?: FixturePack | readonly FixturePack[]; serve?: FixtureServeOptions }[] = [
  { id: "data", label: "the shop (prod audited, staging open)" },
  { id: "deny", label: "policy: deny", overlay: overlays.deny },
  { id: "denied", label: "no db:query grant (PermissionDenied)", overlay: overlays.denied },
  { id: "empty", label: "no databases declared", overlay: overlays.empty },
  { id: "unreachable", label: "describe: datasource unreachable", overlay: overlays.unreachable },
  { id: "timeout", label: "browse: statement timeout", overlay: overlays.timeout },
  { id: "slow", label: "slow answers (loading states)", serve: slow(1500) },
  { id: "many-refs", label: "a customer referenced by 13 tables", overlay: overlays["many-refs"], serve: slow(300) },
];

type PreviewSearch = DataSearch & { env?: string; case?: string; dark?: boolean | string | number; scheme?: string };

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

export default function DataPreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const navigate = useNavigate();
  const caseId = search.case ?? "data";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  const envName = search.env ?? "staging";
  const env = DEMO_ENVS.find((candidate) => candidate.name === envName) ?? DEMO_ENVS[1];
  const dark = !!search.dark && String(search.dark) !== "0" && String(search.dark) !== "false";
  const scheme = search.scheme ?? "reliant";

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);

  // The harness reads as a synthetic reader (as the demo does), so it draws
  // the browser without a signed-in session.
  const [reader] = useState(createFixtureReader);

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
      envs={DEMO_ENVS.map((candidate) => ({ name: candidate.name, preview: false }))}
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
        {/* A fresh transport and cache per case; the browser remounts per environment. */}
        <FixturePreview pack={SHOP} overlay={active.overlay} serve={active.serve ?? SHOP_SERVE}>
          <FixtureReaderProvider reader={reader}>
            <DataBrowser key={`${caseId}-${env.id}`} env={env} search={data} onNavigate={onNavigate} href={href} />
          </FixtureReaderProvider>
        </FixturePreview>
      </div>
    </ConsolePreviewShell>
  );
}
