// Copyright (c) 2025 Reliant Labs

/**
 * api/fileSystem.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the demo has no daemon, so no files. The tour
 * store asks for the file tree only in Reliant's workflows journey.
 */

import type * as Real from "./fileSystem";

export const getFileTree: typeof Real.getFileTree = async () => [];
