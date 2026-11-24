// Copyright (c) 2025 Reliant Labs

/**
 * consoleHost.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): no Reliant projects and nobody signed in. The
 * forge app in the URL is the whole scope.
 */

import type * as Host from "./consoleHost";

export type ConsoleCheckout = Host.ConsoleCheckout;
export type ConsoleReliantProject = Host.ConsoleReliantProject;
export type ConsoleUser = Host.ConsoleUser;

export const consoleCheckout: typeof Host.consoleCheckout = () => ({ kind: "none" });

export const useConsoleReliantProject: typeof Host.useConsoleReliantProject = () => ({ listed: true, projectId: null, candidates: null });

export const useConsoleUser: typeof Host.useConsoleUser = () => undefined;
