// Copyright (c) 2025 Reliant Labs

/**
 * The roster's environments as the observability pages address them, with
 * what each one DECLARES about alerting filled in from its declared shape.
 *
 * The Live roster carries only whether a shape exists; the shape itself is a
 * per-environment document (GetEnvironmentShape). So the pages that read
 * rules, routes or opt-outs — Alerts, and the dashboards and metrics pages
 * that can turn a panel into a rule — fetch it here, and the pages that do not
 * pass `enabled: false` and never request it.
 */

import { useMemo } from "react";

import { useEnvironmentShapes } from "@/hooks/forge-queries";

import type { ObsEnvironment } from "./obsPlace";

export function useDeclaredEnvironments(
  envs: readonly Omit<ObsEnvironment, "declared" | "declaredPending">[],
  { enabled = true }: { enabled?: boolean } = {},
): ObsEnvironment[] {
  const shapes = useEnvironmentShapes(enabled ? envs.map((env) => env.id) : []);
  return useMemo(
    () =>
      envs.map((env) => {
        const entry = shapes.get(env.id);
        return {
          ...env,
          declared: entry?.shape,
          declaredPending: !!entry?.isLoading,
        };
      }),
    [envs, shapes],
  );
}
