// Copyright (c) 2025 Reliant Labs

/**
 * lib/proxy-session.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The real module signs a preview URL in to the
 * workspace proxy by minting a session at the control plane with the
 * signed-in user's credential (Supabase or the forge login broker). Nobody is
 * signed in to the demo, so a preview opens as it is, which is the real
 * module's own answer when there is no session.
 */

import type * as Real from "./proxy-session";

export const signedInPreviewUrl: typeof Real.signedInPreviewUrl = async (previewUrl) => previewUrl;

export const mintProxySessionCallbackUrl: typeof Real.mintProxySessionCallbackUrl = async () => {
  throw new Error("Not signed in.");
};
