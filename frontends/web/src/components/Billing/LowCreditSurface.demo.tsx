// Copyright (c) 2025 Reliant Labs

/**
 * components/Billing/LowCreditSurface.tsx in the forge demo's bundle (a demo
 * twin, vite-plugin-demo-bundle.ts): the demo has no wallet, so the header's
 * low-credit indicator and out-of-credit modal never show. The real surface
 * reads the signed-in account's wallet and opens a Stripe top-up, billing code
 * the demo bundle must not contain.
 */

import type * as Real from "./LowCreditSurface";

export const LowCreditSurface: typeof Real.LowCreditSurface = () => <></>;
