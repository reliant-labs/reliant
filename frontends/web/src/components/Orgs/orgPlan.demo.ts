// Copyright (c) 2026 Reliant Labs

/**
 * components/Orgs/orgPlan.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): nobody signs in to the demo, so there is no
 * org and no plan, and the badge stays hidden. The real hook reads the
 * billing plan through store/authStore, which the demo bundle must not contain.
 */

import type * as Real from "./orgPlan";

export const useOrgPlanLabel: typeof Real.useOrgPlanLabel = () => null;
