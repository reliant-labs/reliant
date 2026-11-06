// Copyright (c) 2025 Reliant Labs

/**
 * components/runs/useOpenRun.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): runs are Reliant chats, and the demo has none.
 */

import { toast } from "@/lib/toast-manager";

import type * as Real from "./useOpenRun";

export const useOpenRun: typeof Real.useOpenRun = () => async () => {
  void toast.error(new Error("Runs open in Reliant, not in the forge demo."));
};
