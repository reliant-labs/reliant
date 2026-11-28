// Copyright (c) 2025 Reliant Labs

/**
 * services/controlPlane/pairing.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). Pairing a machine approves a CLI's code with
 * the signed-in user's credential (Supabase or the forge login broker), at a
 * control-plane URL. The demo has no account to pair a machine to and sends
 * no request off the page's origin, so a lookup or an answer fails with the
 * same PairingError the app shows; codes still format as they do in the app.
 */

import type * as Real from "./pairing";
import { PairingError } from "./pairingCode";

export type PendingKind = Real.PendingKind;
export type PendingMachine = Real.PendingMachine;

export { PairingError, formatPairingCode, normalizePairingCode } from "./pairingCode";

const NO_ACCOUNT = "Pairing a machine needs a Reliant account. The demo runs on sample data, with no sign-in.";

export const lookupPendingMachine: typeof Real.lookupPendingMachine = async () => {
  throw new PairingError(NO_ACCOUNT);
};

export const decidePendingMachine: typeof Real.decidePendingMachine = async () => {
  throw new PairingError(NO_ACCOUNT);
};
