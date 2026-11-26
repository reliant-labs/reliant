// Copyright (c) 2026 Reliant Labs

/**
 * Where every ML entity lives, so every id on every page is a link
 * (02-views §0.2). The URL carries all state: the view and its drill-down in
 * the path (`/forge/ml/runs/<run>/phases/<phase>`), the tab, attempt, rank,
 * selection and filters in the search (routeSchemas forgeOrgMlSearchSchema).
 *
 * THE ROUTES (one place; each view parses its own `sub`):
 *
 *   runs                                  Training list (experiments, runs)
 *   runs/compare?compare=a,b,c            run comparison
 *   runs/<run>                            run detail (run id or name)
 *   runs/<run>/phases/<phase>[?attempt=n] the run scoped to a phase / attempt
 *   evals[?suite=…&baseline=…]            the matrix
 *   evals/runs/<eval_run>[?task=…]        an eval run; ?task opens its samples
 *   models                                registry
 *   models/<model>                        model detail
 *   models/<model>/<version>              model version
 *   inference                             services
 *   inference/<service>                   service detail (?tab, ?sel)
 *   inference/traces/<trace>              a request trace
 *   fleet                                 fleet overview
 *   fleet/pools/<cluster>/<pool>          pool
 *   fleet/<cluster>/<node>                node
 *   fleet/gpus/<uuid>                     device
 *   queues[?sel=<queue|workload>]         queues
 *   recommendations[?sel=<finding>]       inbox, side panel open on ?sel
 *   setup[?sel=<cluster>]                 coverage per cluster
 *   overview                              home
 *   runs/<run>/traces/<trace>             a training trace (also ?tab=traces&sel=)
 */

import { useCallback } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import type { z } from "zod";

import { MlSubjectKind, type MlSubject } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { forgeOrgMlSearchSchema } from "@/routeSchemas";

import { orgPath, type MlView } from "../../Console/paths";

export type MlSearch = z.infer<typeof forgeOrgMlSearchSchema>;

/** `?a=1&b=2` from a search object (unset and empty values dropped), or "". */
export function searchString(search: Partial<MlSearch> | undefined): string {
  if (!search) return "";
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(search)) {
    if (value === undefined || value === null || value === "") continue;
    params.set(key, String(value));
  }
  const s = params.toString();
  return s ? `?${s}` : "";
}

/** The href of an ML page: a view, its drill-down segments, and its search. */
export function mlHref(view: MlView, sub: readonly (string | number)[] = [], search?: Partial<MlSearch>): string {
  return `${orgPath({ kind: "ml", view, sub: sub.map(String) })}${searchString(search)}`;
}

/** Splits a composite subject id ("<run_id>/<phase>/<n>") into its parts. */
const parts = (id: string) => id.split("/");

/**
 * The page that shows `subject`, or null when the ML tab has none (a
 * dataset, a pod, the org itself). A RUN subject's label is its name, and
 * the URL uses it when set; everything else uses the stable id.
 */
export function subjectHref(subject: Pick<MlSubject, "kind" | "id"> & { label?: string }): string | null {
  const { kind, id } = subject;
  if (!id && kind !== MlSubjectKind.ORG) return null;
  switch (kind) {
    case MlSubjectKind.ORG:
      return mlHref("overview");
    case MlSubjectKind.CLUSTER:
      return mlHref("fleet", [], { filter: id });
    case MlSubjectKind.POOL: {
      const [cluster, pool] = parts(id);
      return mlHref("fleet", ["pools", cluster, pool]);
    }
    case MlSubjectKind.NODE: {
      const [cluster, node] = parts(id);
      return mlHref("fleet", [cluster, node]);
    }
    case MlSubjectKind.DEVICE:
      return mlHref("fleet", ["gpus", id]);
    case MlSubjectKind.EXPERIMENT:
      return mlHref("runs", [], { group: "experiment", filter: id });
    case MlSubjectKind.RUN:
      return mlHref("runs", [subject.label && !subject.label.includes(" ") ? subject.label : id]);
    case MlSubjectKind.PHASE: {
      const [run, phase] = parts(id);
      return mlHref("runs", [run, "phases", phase]);
    }
    case MlSubjectKind.ATTEMPT: {
      const [run, phase, n] = parts(id);
      return mlHref("runs", [run, "phases", phase], { attempt: Number(n) });
    }
    case MlSubjectKind.RANK: {
      const [run, phase, n, rank] = parts(id);
      return mlHref("runs", [run, "phases", phase], { attempt: Number(n), rank: Number(rank), tab: "system" });
    }
    case MlSubjectKind.CHECKPOINT: {
      const [run] = parts(id);
      return mlHref("runs", [run], { tab: "checkpoints", sel: id });
    }
    case MlSubjectKind.MODEL:
      return mlHref("models", [id]);
    case MlSubjectKind.MODEL_VERSION: {
      const [model, version] = parts(id);
      return mlHref("models", [model, version]);
    }
    case MlSubjectKind.EVAL_RUN:
      return mlHref("evals", ["runs", id]);
    case MlSubjectKind.INFERENCE_SERVICE:
      return mlHref("inference", [id]);
    case MlSubjectKind.REVISION: {
      const [service] = parts(id);
      return mlHref("inference", [service], { tab: "revisions", sel: id });
    }
    case MlSubjectKind.REPLICA: {
      const [service] = parts(id);
      return mlHref("inference", [service], { tab: "replicas", sel: id });
    }
    case MlSubjectKind.QUEUE:
    case MlSubjectKind.WORKLOAD:
      return mlHref("queues", [], { sel: id });
    case MlSubjectKind.FINDING:
      return mlHref("recommendations", [], { sel: id });
    default:
      return null;
  }
}

/**
 * The page's ML search, and a setter that merges a patch into it (replacing
 * history by default: tabs and selections are not steps back).
 */
export function useMlSearch(): [MlSearch, (patch: Partial<MlSearch>, opts?: { push?: boolean }) => void] {
  const search = useSearch({ strict: false }) as MlSearch;
  const navigate = useNavigate();
  const set = useCallback(
    (patch: Partial<MlSearch>, opts?: { push?: boolean }) =>
      void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, ...patch }), replace: !opts?.push } as never),
    [navigate],
  );
  return [search, set];
}
