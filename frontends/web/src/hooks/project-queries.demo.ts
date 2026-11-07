// Copyright (c) 2025 Reliant Labs

/**
 * hooks/project-queries.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the demo has no Reliant projects. The forge
 * app in the URL is the console's whole scope.
 */

import type * as Real from "./project-queries";

export type Project = Real.Project;
export type ProjectListView = Real.ProjectListView;

export const useProjects: typeof Real.useProjects = () => ({ projects: [], isLoading: false, error: null, refetch: () => {} });

export const useProject: typeof Real.useProject = () => undefined;

export const fetchProjects: typeof Real.fetchProjects = async () => [];

export const createProjectFromRepo: typeof Real.createProjectFromRepo = async () => {
  throw new Error("Cloning a project needs Reliant; the forge demo has no machines.");
};

export const getCachedProjects: typeof Real.getCachedProjects = () => [];
