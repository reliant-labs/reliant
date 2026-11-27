// Copyright (c) 2026 Reliant Labs

/**
 * components/Orgs/orgPlan.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the sample org (hooks/useOrgs.demo.ts, Acme) is
 * on the Starter plan, as the hosted preview of the app shows it ("Acme ·
 * Starter"), answered from this constant; any other org has no badge. The
 * real hook reads the billing plan through the cloud billing queries, which
 * reach lib/auth, which the demo bundle must not contain.
 */

import { DEMO_ORG } from "@/fixtures/shop/shopStory";

import type * as Real from "./orgPlan";

export const useOrgPlanLabel: typeof Real.useOrgPlanLabel = (activeOrgId) => (activeOrgId === DEMO_ORG ? "Starter" : null);
