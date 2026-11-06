// Copyright (c) 2025 Reliant Labs

/**
 * consoleHost.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): no Reliant projects and nobody signed in. The
 * forge app in the URL is the whole scope.
 */

import type * as Host from "./consoleHost";

export type ConsoleReliantProject = Host.ConsoleReliantProject;
export type ConsoleUser = Host.ConsoleUser;

export const projectDeclaring: typeof Host.projectDeclaring = () => null;

export const useConsoleReliantProject: typeof Host.useConsoleReliantProject = () => ({ listed: true, projectId: null });

export const useConsoleUser: typeof Host.useConsoleUser = () => undefined;
