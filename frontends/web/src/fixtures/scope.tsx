// Copyright (c) 2026 Reliant Labs

/**
 * WHICH APPS READ SAMPLE DATA, and the one place that decides it.
 *
 * The forge tour walks a sample app inside the signed-in shell
 * (Forge/Sample/sampleApp.ts: `sample_shop`, labelled "shop (sample)"). Its
 * console pages, and the shell nav's model of its sections, read the shop
 * fixture pack through a TransportScope of their own (lib/transportScope.tsx)
 * while the person's chats, projects and org beside them stay live:
 *
 *   appScope(app)        null for every real app; for the sample, ONE
 *                        memoized scope (the nav and the page share its
 *                        cache): the shop pack told for `sample_shop`, in any
 *                        org, every id it answers prefixed `sample_`, writes
 *                        refused, with the app's query defaults.
 *   AppScopeProvider     what ConsoleLayout and the shell nav's console model
 *                        mount: the scope, read-only console controls
 *                        (ObsReadOnlyProvider) and Ask in handoff mode (no
 *                        chat is started about sample data).
 *   withSampleApp        the roster decorator: the sample's row in the app
 *                        catalog (the switcher), only while the tour is
 *                        active. Never fetched, never persisted.
 *   the tour flag        useSampleTour / setSampleTourActive: set by the
 *                        forge journey while it walks the sample; on its way
 *                        down the sample's cache is cleared.
 *
 * The backstop beside this is lib/sampleGuard.ts: the live transport refuses
 * any request carrying the sample's name or one of its ids, so a read that
 * escapes the scope fails closed.
 *
 * The pack is imported lazily, on the scope's first rpc, so the app's own
 * bundle does not carry it until someone opens the sample.
 */

import { useMemo, type ReactNode } from "react";
import type { Transport } from "@connectrpc/connect";
import { create } from "zustand";

import { AskAgentModeProvider } from "@/components/Forge/Observability/AskReliant/askAgentMode";
import { ObsReadOnlyProvider, type ObsReadOnly } from "@/components/Forge/Observability/obsReadOnly";
import { SAMPLE_APP, SAMPLE_APP_LABEL, SAMPLE_ID_PREFIX, isSampleApp } from "@/components/Forge/Sample/sampleApp";
import { createAppQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider, type TransportScope } from "@/lib/transportScope";
import { appRowKey, type AppOnlyCatalogRow, type ProjectCatalog } from "@/services/projects/catalog";

// ── The scope ───────────────────────────────────────────────────────────────

/** A transport that loads the shop pack on its first call, and answers every call from it. */
function sampleTransport(): Transport {
  let target: Promise<Transport> | null = null;
  const pack = () =>
    (target ??= import("./shop").then(({ createShopTransport }) =>
      createShopTransport({ app: SAMPLE_APP, org: null, idPrefix: SAMPLE_ID_PREFIX }),
    ));
  return {
    async unary(method, signal, timeoutMs, header, input, contextValues) {
      return (await pack()).unary(method, signal, timeoutMs, header, input, contextValues);
    },
    async stream(method, signal, timeoutMs, header, input, contextValues) {
      return (await pack()).stream(method, signal, timeoutMs, header, input, contextValues);
    },
  };
}

let sample: TransportScope | null = null;

/**
 * The TransportScope `app`'s console reads, or null for a real app (which
 * reads the app's transport). The sample's is created once, on first use, and
 * shared by every reader: the nav and the page see one cache.
 */
export function appScope(app: string | null | undefined): TransportScope | null {
  if (!isSampleApp(app)) return null;
  sample ??= createTransportScope({ id: "sample", transport: sampleTransport(), queryClient: createAppQueryClient(), readOnly: true });
  return sample;
}

/** What a disabled control says on the sample's pages. */
export const SAMPLE_READ_ONLY_REASON = "Sample app: changes are turned off";

/**
 * `children` in `app`'s scope: for the sample, its TransportScope, read-only
 * controls and Ask in handoff mode; for a real app, nothing (they stay on the
 * app's transport and cache).
 */
export function AppScopeProvider({ app, children }: { app: string | null | undefined; children: ReactNode }) {
  const scope = appScope(app);
  const readOnly = useMemo<ObsReadOnly>(() => ({ readOnly: scope?.readOnly ?? false, reason: SAMPLE_READ_ONLY_REASON }), [scope]);
  if (!scope) return <>{children}</>;
  return (
    <TransportScopeProvider scope={scope}>
      <ObsReadOnlyProvider value={readOnly}>
        <AskAgentModeProvider mode="handoff" demo>
          {children}
        </AskAgentModeProvider>
      </ObsReadOnlyProvider>
    </TransportScopeProvider>
  );
}

// ── The tour flag ───────────────────────────────────────────────────────────

/**
 * Whether the forge tour is walking the sample right now. Memory only: a
 * reload, or another tab, starts without it. The forge journey sets it when it
 * starts walking the sample in the shell and clears it when it ends (finish,
 * exit, skip); until then nothing here shows the sample anywhere a person's
 * own apps are listed.
 */
export const useSampleTour = create<{ active: boolean }>(() => ({ active: false }));

export function setSampleTourActive(active: boolean): void {
  if (useSampleTour.getState().active !== active) useSampleTour.setState({ active });
}

// Leaving the tour leaves nothing of the sample in memory: its cache goes with it.
useSampleTour.subscribe((state, prev) => {
  if (prev.active && !state.active) sample?.queryClient.clear();
});

// ── The roster decorator ────────────────────────────────────────────────────

/** The sample's row in the app catalog: an app with no code here, deployed to the story's three environments. */
export const SAMPLE_ROW: AppOnlyCatalogRow = {
  kind: "app",
  key: appRowKey(SAMPLE_APP),
  name: SAMPLE_APP_LABEL,
  activityAt: 0,
  app: { name: SAMPLE_APP, link: null, state: "live", isStaticSite: false, environments: ["prod", "staging", "dev"] },
};

/**
 * `catalog` with the sample's row added (once), for the switcher and the
 * shell's scope while the tour walks it. The row is made here, never read
 * from a backend, and nothing writes it anywhere.
 */
export function withSampleApp(catalog: ProjectCatalog): ProjectCatalog {
  if (catalog.rows.some((row) => row.key === SAMPLE_ROW.key)) return catalog;
  return { ...catalog, rows: [...catalog.rows, SAMPLE_ROW] };
}

/** Test seam: forget the sample's scope and the tour flag. */
export function resetSampleScopeForTests(): void {
  sample?.queryClient.clear();
  sample = null;
  useSampleTour.setState({ active: false });
}
