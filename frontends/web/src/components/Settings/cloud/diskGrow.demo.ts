// Copyright (c) 2026 Reliant Labs

/**
 * components/Settings/cloud/diskGrow.ts in the forge demo's bundle (a demo
 * twin, vite-plugin-demo-bundle.ts): the demo's Agent sandboxes page
 * (demoAgentsRoutes.tsx) never grows a disk (it is a paid change, and every
 * write is refused), so a machine's disk has nothing to say about a grow.
 * The real module prices a grow against the plan, billing code the demo
 * bundle must not contain.
 */

import type * as Real from "./diskGrow";

export type DiskGrowStatus = Real.DiskGrowStatus;

export const diskGrowStatus: typeof Real.diskGrowStatus = () => null;

export const isStorageDenied: typeof Real.isStorageDenied = () => false;
