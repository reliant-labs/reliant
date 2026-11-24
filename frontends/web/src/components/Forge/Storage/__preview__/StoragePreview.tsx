// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the storage browser. NOT a product surface.
 *
 * The real StorageBrowser, inside the real console chrome
 * (ConsolePreviewShell), on its real client over the shop fixture pack
 * (src/fixtures/shop, <FixturePreview>) — unauthenticated, no backend — so
 * every state can be looked at in every colour scheme and screenshotted. A
 * case is a named overlay over the pack (src/fixtures/shop/overlays.ts
 * browserOverlays.storage). The browser's state is this page's own URL, as
 * it is the console's, so a screenshot is a link:
 *
 *   /forge-storage-preview                               staging (open), the uploads bucket
 *   …?env=prod                                           prod: audited, asks for a reason
 *   …?env=dev                                            an environment that declares no bucket
 *   …&object=orders%2Fsample.json                        a JSON preview
 *   …&case=deny|denied|empty|buckets-down|shape|rate|loading|slow
 *   …&dark=1&scheme=pure-black
 *
 * Download is refused, as everywhere on the pack: it mints no URL.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import PageHeader from "@/components/forge-ui/page_header";
import { latency } from "@/fixtures/overlay";
import { FixturePreview, type FixtureServeOptions } from "@/fixtures/preview";
import { SHOP_SERVE, shopPack } from "@/fixtures/shop";
import { DEMO_ENVS } from "@/fixtures/shop/handlers/scope";
import { browserOverlays } from "@/fixtures/shop/overlays";
import type { FixturePack } from "@/fixtures/transport";

import { createFixtureReader, FixtureReaderProvider } from "../../AuditedAccess/readerSession";
import { ConsolePreviewShell } from "../../Console/ConsolePreviewShell";
import { withSearch } from "../../Console/paths";
import { StorageBrowser } from "../StorageBrowser";
import { pickStorageSearch, type StorageSearch } from "../storageUrl-model";

const BASE = "/forge-storage-preview";

const SCHEMES = ["reliant", "professional-blue", "refined-neutral", "modern-teal", "slate", "purple-classic", "pure-black"] as const;

const SHOP = shopPack();
const overlays = browserOverlays.storage;
const SLOW: FixtureServeOptions = { ...SHOP_SERVE, interceptors: [latency(1500)] };

const CASES: { id: string; label: string; overlay?: FixturePack | readonly FixturePack[]; serve?: FixtureServeOptions }[] = [
  { id: "storage", label: "the buckets (prod audited, staging open)" },
  { id: "deny", label: "policy: deny", overlay: overlays.deny },
  { id: "denied", label: "no storage:read grant (PermissionDenied)", overlay: overlays.denied },
  { id: "empty", label: "no buckets declared", overlay: overlays.empty },
  { id: "buckets-down", label: "bucket list: unavailable", overlay: overlays["buckets-down"] },
  { id: "shape", label: "no recorded release (shape_unknown)", overlay: overlays.shape },
  { id: "rate", label: "listing: rate limited", overlay: overlays.rate },
  { id: "loading", label: "bucket list: loading (never answers)", overlay: overlays.loading },
  { id: "slow", label: "slow answers (loading states)", serve: SLOW },
];

type PreviewSearch = StorageSearch & { env?: string; case?: string; dark?: boolean | string | number; scheme?: string };

const CONTROL = "rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink";

export default function StoragePreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const navigate = useNavigate();
  const caseId = search.case ?? "storage";
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0]!;
  const envName = search.env ?? "staging";
  const env = DEMO_ENVS.find((candidate) => candidate.name === envName) ?? DEMO_ENVS[1];
  const dark = !!search.dark && String(search.dark) !== "0" && String(search.dark) !== "false";
  const scheme = search.scheme ?? "reliant";
  // The harness reads as a synthetic reader (as the demo does), so it draws
  // the browser without a signed-in session.
  const [reader] = useState(createFixtureReader);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.setAttribute("data-color-scheme", scheme);
  }, [dark, scheme]);

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
      envs={DEMO_ENVS.map((candidate) => ({ name: candidate.name, preview: false }))}
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
        </>
      }
    >
      <div className="space-y-4">
        <PageHeader density="compact" title="Storage" className="mb-0" />
        {/* A fresh transport and cache per case; the browser remounts per environment. */}
        <FixturePreview pack={SHOP} overlay={active.overlay} serve={active.serve ?? SHOP_SERVE}>
          <FixtureReaderProvider reader={reader}>
            <StorageBrowser key={`${caseId}-${env.id}`} env={env} search={storage} onNavigate={onNavigate} href={href} />
          </FixtureReaderProvider>
        </FixturePreview>
      </div>
    </ConsolePreviewShell>
  );
}
