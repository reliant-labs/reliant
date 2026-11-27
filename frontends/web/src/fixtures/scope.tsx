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
 *   the tour flag        useSampleTour / setSampleTourActive
 *                        (Forge/Sample/sampleTour.ts): set by the forge
 *                        journey while it walks the sample; on its way down
 *                        the sample's cache is cleared.
 *
 * The backstop beside this is lib/sampleGuard.ts: the live transport refuses
 * any request carrying the sample's name or one of its ids, so a read that
 * escapes the scope fails closed.
 *
 * The pack is imported lazily, on the scope's first rpc, so the app's own
 * bundle does not carry it until someone opens the sample.
 */

import { useMemo, type ReactNode } from "react";
import type { DescMethod } from "@bufbuild/protobuf";
import type { Transport } from "@connectrpc/connect";

import { AskAgentModeProvider } from "@/components/Forge/Observability/AskReliant/askAgentMode";
import { ObsReadOnlyProvider, type ObsReadOnly } from "@/components/Forge/Observability/obsReadOnly";
import { SAMPLE_APP, SAMPLE_APP_LABEL, SAMPLE_ID_PREFIX, isSampleApp } from "@/components/Forge/Sample/sampleApp";
import { setSampleTourActive, useSampleTour } from "@/components/Forge/Sample/sampleTour";
import { transport as appTransport } from "@/lib/connect";
import { createAppQueryClient } from "@/lib/query-client";
import { createTransportScope, TransportScopeProvider, type TransportScope } from "@/lib/transportScope";
import { appRowKey, type AppOnlyCatalogRow, type ProjectCatalog } from "@/services/projects/catalog";

// ── The scope ───────────────────────────────────────────────────────────────

/**
 * The sample's transport: a call to a service the shop pack owns
 * (SHOP_PACK_SERVICES) is answered from the pack, loaded on the scope's first
 * rpc; every other call (the person's projects, chats, the shell's own reads
 * that a generated hook makes under the scope) goes to the app's transport,
 * where lib/sampleGuard.ts refuses anything that names the sample. Routing by
 * service, not by rpc, is what keeps "the person's data stays live" true for a
 * read nobody listed.
 */
function sampleTransport(): Transport {
  let target: Promise<{ pack: Transport; owned: ReadonlySet<string> }> | null = null;
  const load = () =>
    (target ??= import("./shop").then(({ createShopTransport, SHOP_PACK_SERVICES }) => ({
      pack: createShopTransport({ app: SAMPLE_APP, org: null, idPrefix: SAMPLE_ID_PREFIX }),
      owned: new Set(SHOP_PACK_SERVICES.map((service) => service.typeName)),
    })));
  const route = async (method: DescMethod): Promise<Transport> => {
    const { pack, owned } = await load();
    return owned.has(method.parent.typeName) ? pack : appTransport;
  };
  return {
    async unary(method, signal, timeoutMs, header, input, contextValues) {
      return (await route(method)).unary(method, signal, timeoutMs, header, input, contextValues);
    },
    async stream(method, signal, timeoutMs, header, input, contextValues) {
      return (await route(method)).stream(method, signal, timeoutMs, header, input, contextValues);
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

// The flag itself is Forge/Sample/sampleTour.ts (the journey sets it while it walks the sample); re-exported here.
export { setSampleTourActive, useSampleTour };

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
