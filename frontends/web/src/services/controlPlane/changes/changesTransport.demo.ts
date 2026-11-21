// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's twin of changesTransport.ts (vite-plugin-demo-bundle.ts,
 * DEMO TWINS): the demo has no backend and nobody signed in, so every
 * ChangesService call answers Unimplemented and the Experiments page shows
 * its "not available here yet" state.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { SAMPLE_DATA_UNAVAILABLE } from "@/lib/connect.demo";

import type * as Real from "./changesTransport";

export const CHANGES_SERVICE: typeof Real.CHANGES_SERVICE = "controlplane.v1.ChangesService";

export type ChangesCall = Real.ChangesCall;

export const connectErrorOf: typeof Real.connectErrorOf = (_status, headers, body) => {
  const message = body && typeof body === "object" && typeof (body as { message?: unknown }).message === "string" ? (body as { message: string }).message : SAMPLE_DATA_UNAVAILABLE;
  return new ConnectError(message, Code.Unimplemented, headers);
};

export const callChanges: ChangesCall = async () => {
  throw new ConnectError(SAMPLE_DATA_UNAVAILABLE, Code.Unimplemented);
};
