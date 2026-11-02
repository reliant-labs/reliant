// Copyright (c) 2026 Reliant Labs

/**
 * Built-in dashboards this client owns rather than the control plane.
 *
 * "LLM usage" (`org-llm`) is the caller's own model usage, served by reliant
 * (`UsageService.GetLLMUsage`, caller-scoped), not by control-plane's
 * DashboardService: its gate is being signed in, not billing:read, so the
 * control plane's built-in list (which it filters by billing:read) is the
 * wrong owner for it. It is listed in a project's dashboards (see
 * withClientBuiltins) and opened without a DashboardService request.
 */

import type { DashboardDocument, DashboardIndex, DashboardRef, DashboardSummary } from "./dashboardTypes";

export const LLM_USAGE_KEY = "org-llm";

export const LLM_USAGE_SUMMARY: DashboardSummary = {
  ref: { kind: "builtin", projectId: "", key: LLM_USAGE_KEY },
  source: "builtin",
  viewKind: "llm-usage",
  group: "product",
  title: "LLM usage",
  description: "Your model calls across Reliant AI, your API keys, subscriptions and local models, with the organization's Reliant AI spend.",
  readOnly: true,
  starred: false,
  lastViewedAtMs: null,
  panelCount: 0,
  previewPanels: [],
  release: null,
  workspace: null,
  category: "billing",
};

export function isClientBuiltin(ref: DashboardRef | null | undefined): boolean {
  return !!ref && ref.kind === "builtin" && ref.key === LLM_USAGE_KEY;
}

export function clientBuiltinDocument(): DashboardDocument {
  return { summary: LLM_USAGE_SUMMARY, spec: { id: LLM_USAGE_KEY, title: LLM_USAGE_SUMMARY.title, filters: [], annotations: [], panels: [] }, version: 0, canWrite: false };
}

/**
 * The index with the client's built-ins added, after any the server listed.
 *
 * Only a project's list gets them: a built-in opens inside a project's
 * console (`…/dashboards/builtin/<key>`), and the organization-wide list has
 * no project to open it in. Only a list that already holds a dashboard: an
 * empty project keeps its getting-started page instead of a list of one
 * built-in. Never twice.
 */
export function withClientBuiltins(index: DashboardIndex, scope: { projectId: string | undefined; envId: string | undefined }): DashboardIndex {
  if (!scope.projectId || index.dashboards.length === 0) return index;
  if (index.dashboards.some((d) => isClientBuiltin(d.ref))) return index;
  const lastBuiltin = index.dashboards.reduce((at, d, i) => (d.source === "builtin" ? i : at), -1);
  const dashboards = [...index.dashboards];
  dashboards.splice(lastBuiltin + 1, 0, LLM_USAGE_SUMMARY);
  return { ...index, dashboards };
}
