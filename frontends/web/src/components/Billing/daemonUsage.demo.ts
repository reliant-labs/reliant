// Copyright (c) 2026 Reliant Labs

/**
 * components/Billing/daemonUsage.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the demo's Agent sandboxes page
 * (demoAgentsRoutes.tsx) shows the shop's machines with no plan or price
 * list, so there are no included hours and no pricing footnote. Only a
 * machine's provisioned disk is said, as the real module says it. The real
 * module is the client half of per-machine billing, which the demo bundle
 * must not contain.
 */

import type * as Real from "./daemonUsage";

export type DaemonPricingLike = Real.DaemonPricingLike;

export const provisionedDiskGib: typeof Real.provisionedDiskGib = (disk) => (disk && disk.capacityGib > 0 ? disk.capacityGib : null);

export const planOwnSizeHours: typeof Real.planOwnSizeHours = () => null;

export const machinePricingFootnote: typeof Real.machinePricingFootnote = () => "";
