// Copyright (c) 2025 Reliant Labs

/**
 * store/projectStore.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the demo has no Reliant projects, so there is
 * never a current one, and nothing can create, change or open one.
 */

import { create } from "zustand";

import type * as Real from "./projectStore";

type ProjectStore = ReturnType<typeof Real.useProjectStore.getState>;
export type Project = Real.Project;

const none = async (): Promise<never> => {
  throw new Error("Reliant projects are not part of the forge demo.");
};

export const useProjectStore: typeof Real.useProjectStore = create<ProjectStore>()(() => ({
  currentProject: null,
  selectProject: async () => {},
  setProjectFilter: async () => {},
  restoreLastProject: async () => false,
  refreshCurrentProject: async () => {},
  createProject: none,
  updateProject: none,
  deleteProject: none,
  reset: () => {},
}));
