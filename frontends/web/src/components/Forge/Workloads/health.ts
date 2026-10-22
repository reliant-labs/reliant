// Copyright (c) 2025 Reliant Labs

/**
 * How the server's workload verdict (WorkloadHealth) reads on screen: one
 * dot tone and one word per verdict, shared by every surface that shows a
 * workload's health — the Overview, the workload page and the Observability
 * home — so none of them can word it differently.
 */

import type { StatusTone } from "@/components/forge-ui/status";
import type { WorkloadHealth } from "@/services/controlPlane/observability/types";

export const HEALTH_STATUS: Record<WorkloadHealth, { tone: StatusTone; label: string }> = {
  healthy: { tone: "success", label: "Healthy" },
  degraded: { tone: "warning", label: "Degraded" },
  crash_looping: { tone: "danger", label: "Crash-looping" },
  pending: { tone: "neutral", label: "Pending" },
  suspended: { tone: "neutral", label: "Suspended" },
  unknown: { tone: "neutral", label: "Unknown" },
};
