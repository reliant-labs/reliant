// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY: the dev `__preview__` pages' one data seam. `<FixturePreview>`
 * renders its subtree over a fixture transport (./transport.ts) serving
 * `pack`, with `overlay` (named states the pack does not have, e.g.
 * shop/overlays.ts) on top, so the page's REAL components run their DEFAULT
 * clients (`connectClient` over lib/connect.ts) against the pack: no
 * `*ApiProvider` override, no per-feature fixture copy.
 *
 *   <FixturePreview pack={SHOP} serve={SHOP_SERVE} overlay={shopOverlays.obs["not-set-up"]}>
 *     <ObservabilityPanel … />
 *   </FixturePreview>
 *
 * THE TRANSPORT IS GLOBAL. lib/connect.ts resolves every call's transport
 * from a module singleton, so this installs the fixture transport through its
 * seam (`setTransport`) while mounted and restores the one it replaced on
 * unmount. Two previews mounted at once would fight over that singleton, so a
 * second one throws instead. The subtree renders only once the transport is
 * installed, and gets a QueryClient of its own: a change of pack, overlay or
 * serving options builds a new transport and remounts the subtree with an
 * empty cache, so one state's answers never leak into the next.
 *
 * `signedIn` puts a fake session in the auth store while mounted, for the
 * app-layer surfaces that read only for a session (the projects list, the
 * console's roster). Its token never leaves the browser: the fixture
 * transport runs none of the app's interceptors.
 *
 * `pack`, `overlay` and `serve` must be stable (module constants, or
 * memoized): a new object is a new transport.
 *
 * Only the `__preview__` pages import this, and they are lazy dev-only routes
 * (routes.tsx DevOnlyRoute), so it never reaches the app's or the demo's
 * entry chunk.
 */

import { useLayoutEffect, useMemo, useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Transport } from "@connectrpc/connect";

import { setTransport } from "@/lib/connect";
import { useAuthStore } from "@/store/authStore";

import { createFixtureTransport, defineFixturePack, type CreateFixtureTransportOptions, type FixturePack } from "./transport";

/** How a pack is served (everything but the pack): the clock, the read-only guard, interceptors. */
export type FixtureServeOptions = Omit<CreateFixtureTransportOptions, "pack">;

export interface FixturePreviewProps {
  /** The world to render (shop/index.ts shopPack, shop/app.ts shopAppPack). */
  // `any`: a preview serves a pack over any data map.
  pack: FixturePack<any>;
  /** Named states over the pack, applied in order after it (a later layer wins, per method). */
  overlay?: FixturePack<any> | readonly FixturePack<any>[] | null;
  /** How the pack is served (shop/index.ts SHOP_SERVE for the shop). */
  serve?: FixtureServeOptions;
  /** Render as a signed-in user (a fake session in the auth store, restored on unmount). */
  signedIn?: boolean;
  children: ReactNode;
}

/** `pack` with `overlay` on top, as one pack. */
export function withOverlay(pack: FixturePack<any>, overlay?: FixturePack<any> | readonly FixturePack<any>[] | null): FixturePack<any> {
  const layers: readonly FixturePack<any>[] = overlay == null ? [] : Array.isArray(overlay) ? overlay : [overlay as FixturePack<any>];
  if (layers.length === 0) return pack;
  return defineFixturePack({ name: [pack.name, ...layers.map((layer) => layer.name)].join("+"), extends: [pack, ...layers] });
}

/** The preview that owns the module transport now, if any. */
let owner: object | null = null;

export function FixturePreview({ pack, overlay, serve, signedIn = false, children }: FixturePreviewProps) {
  const transport = useMemo(() => createFixtureTransport({ ...serve, pack: withOverlay(pack, overlay) }), [pack, overlay, serve]);
  const queryClient = useMemo(
    () => new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } }),
    // A fresh cache per transport: the answers it holds were that transport's.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [transport]
  );
  const [installed, setInstalled] = useState<Transport | null>(null);

  useLayoutEffect(() => {
    if (owner) {
      throw new Error("FixturePreview: another fixture preview is mounted. The Connect transport is a module singleton (lib/connect.ts), so only one preview may own it at a time.");
    }
    const me = {};
    owner = me;
    const restore = setTransport(transport);
    const originalSession = useAuthStore.getState().session;
    const fakeSession = { access_token: "fixture-preview" } as NonNullable<typeof originalSession>;
    if (signedIn) useAuthStore.setState({ session: fakeSession });
    setInstalled(transport);
    return () => {
      restore();
      if (signedIn && useAuthStore.getState().session === fakeSession) useAuthStore.setState({ session: originalSession });
      if (owner === me) owner = null;
    };
  }, [transport, signedIn]);

  // Nothing renders until the transport is in: a child's first read must not reach the real one.
  if (installed !== transport) return null;
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}
