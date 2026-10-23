// Fixture: Zustand store that ALSO imports a generated Connect client.
// This is the canonical foot-gun the rule exists to catch — server data
// in client-only state. Lint should fire on the create<...> line.

import { create } from "zustand";
import { BillingServiceClient } from "../../gen/billing/v1/billing_connect";

type BillingState = {
  invoices: number;
};

export const useBillingStore = create<BillingState>(() => ({
  invoices: 0,
}));

export type _UnusedClientRef = BillingServiceClient;
