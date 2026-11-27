// Copyright (c) 2026 Reliant Labs

/**
 * components/Settings/cloud/GrowDiskModal.tsx in the forge demo's bundle (a
 * demo twin, vite-plugin-demo-bundle.ts): the demo's Agent sandboxes page
 * (demoAgentsRoutes.tsx) never grows a disk (diskGrow.demo.ts), so neither
 * the grow dialog nor the disk's grow notice ever shows. The real dialog
 * prices the grow against the plan, billing code the demo bundle must not
 * contain.
 */

import type * as Real from "./GrowDiskModal";

export type GrowStage = Real.GrowStage;

export const GrowDiskModal: typeof Real.GrowDiskModal = () => <></>;

export const DiskGrowNotice: typeof Real.DiskGrowNotice = () => <></>;
