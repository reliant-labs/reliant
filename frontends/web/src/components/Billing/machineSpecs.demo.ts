// Copyright (c) 2026 Reliant Labs

/**
 * components/Billing/machineSpecs.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the demo's Agent sandboxes page
 * (demoAgentsRoutes.tsx) cannot resize a machine (every write is refused), so
 * its size picker names the sizes without the catalog's specs. The real
 * module is the machine catalog checkout prices against, billing code the
 * demo bundle must not contain.
 */

import type * as Real from "./machineSpecs";

export const formatMachineSpec: typeof Real.formatMachineSpec = () => undefined;
