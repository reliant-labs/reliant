// Fixture: re-export from a generated Connect client without spinning
// up a Zustand store. The file lives under stores/ for organizational
// reasons; no client-state is in play. Lint must NOT fire.

import type { BillingServiceClient } from "../../gen/billing/v1/billing_connect";

export type { BillingServiceClient };
