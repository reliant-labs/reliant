// Copyright (c) 2025 Reliant Labs

/**
 * hooks/project-catalog-queries.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): nobody signs in to the demo, so the real hook
 * never asks for a catalog there; this answers the same "nothing, not
 * loading" without reading store/authStore, which the demo bundle must not
 * contain. The selector lists the demo's apps from the list it is passed.
 */

import type * as Real from "./project-catalog-queries";

export type ProjectCatalogView = Real.ProjectCatalogView;

export const useProjectCatalog: typeof Real.useProjectCatalog = () => ({
  catalog: undefined,
  isLoading: false,
  error: null,
  refetch: () => {},
});
