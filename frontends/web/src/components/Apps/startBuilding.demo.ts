// Copyright (c) 2025 Reliant Labs

/**
 * components/Apps/startBuilding.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): an app is built in a Reliant chat, and the demo
 * has no chats, so "Add an app" says where that happens instead of opening one.
 */

import { toast } from "@/lib/toast-manager";

import type * as Real from "./startBuilding";

export const useStartBuildingApp: typeof Real.useStartBuildingApp = () => () => {
  void toast.error(new Error("Apps are built in Reliant, not in the forge demo."));
};
