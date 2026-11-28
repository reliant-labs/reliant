// Copyright (c) 2025 Reliant Labs

/**
 * api/writeOwner.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The real module checks a write's owner against
 * the bearer token on the request about to leave (lib/auth/sessionOwner's
 * credentialBelongsTo). The demo sends no credential and has no interceptor
 * chain: every call goes to the demo's transport, which refuses writes. So a
 * writer may still name its owner on a call (writeOwnerKey), nothing checks
 * it, and no write is ever dropped for a credential mismatch.
 */

import { createContextKey } from "@connectrpc/connect";

import type { SessionOwner } from "../lib/auth/sessionOwner";
import type * as Real from "./writeOwner";

export const writeOwnerKey: typeof Real.writeOwnerKey = createContextKey<SessionOwner | null>(null, {
  description: "the account a write was made for",
});

export const isWriteOwnerMismatch: typeof Real.isWriteOwnerMismatch = () => false;
