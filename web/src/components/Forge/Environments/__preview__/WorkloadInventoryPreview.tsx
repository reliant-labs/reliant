// Copyright (c) 2025 Reliant Labs

/**
 * Proving harness for the workload inventory. NOT a product surface.
 *
 * It exists for the same reason ForgeTokenSandbox does, and for one more: the
 * complaint this screen fixes was VISUAL — a user looked at a page and drew a
 * false conclusion from it — so "the tests pass" is not evidence that it is
 * fixed. This route renders the real component against REAL CAPTURED FORGE
 * OUTPUT, unauthenticated, with a scheme and light/dark switcher, so the thing
 * the user will actually see can be looked at in every theme.
 *
 * The documents in capturedReports.ts came out of `forge env status --json`
 * against control-plane's live GKE prod and k3d dev. Nothing here is
 * hand-written, which is the point: the shapes that broke the old screen (a
 * Job with no `desired_replicas`, an unreachable cluster that still lists
 * sixteen workloads) are present because the cluster produced them, not
 * because someone thought to mock them.
 *
 * `.forge-ui` wraps the render, exactly as ForgeLayout does in the product —
 * without it forge's `accent` resolves to reliant's muted hover tint and every
 * accent-colored forge component renders as a barely-visible grey.
 */

import { useState } from "react";

import type { ForgeOutcome } from "@/services/forge/topology";
import type { ForgeEnvStatusReport } from "@/services/forge/status";
import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

import { ForgeShell } from "../../ForgeShell";
import { WorkloadInventory } from "../WorkloadInventory";
import { DEV_TWO_CLUSTERS, PROD_REACHABLE, PROD_UNREACHABLE } from "./capturedReports";

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "forest",
  "vibrant-pink",
  "energetic-orange",
  "bold-red",
  "purple-classic",
  "pure-black",
] as const;

const CASES = [
  {
    id: "prod",
    label: "prod — reachable (16 workloads)",
    env: "prod",
    report: PROD_REACHABLE,
  },
  {
    id: "prod-unreachable",
    label: "prod — cluster unreadable (16 unknown)",
    env: "prod",
    report: PROD_UNREACHABLE,
  },
  {
    id: "dev",
    label: "dev — two clusters",
    env: "dev",
    report: DEV_TWO_CLUSTERS,
  },
  {
    id: "nothing",
    label: "an env that deploys nothing",
    env: "e2e",
    report: {
      env: "e2e",
      services: [],
      workloads: { status: "pass", env: "e2e", clusters: [], workloads: [] },
    } as ForgeEnvStatusReport,
  },
  {
    id: "not-reported",
    label: "a forge with no workloads key",
    env: "prod",
    report: { env: "prod", services: [] } as ForgeEnvStatusReport,
  },
] as const;

const META: ForgeReportMeta = {
  isForgeProject: true,
  supported: true,
  forgeVersion: "v0.1.18",
  unsupportedReason: "",
  exitCode: 0,
  reachability: ForgeReachability.UNSPECIFIED,
  unreachableReason: "",
} as ForgeReportMeta;

export default function WorkloadInventoryPreview() {
  const [scheme, setScheme] = useState<string>(
    () => document.documentElement.getAttribute("data-color-scheme") ?? "professional-blue"
  );
  const [dark, setDark] = useState<boolean>(() =>
    document.documentElement.classList.contains("dark")
  );
  const [caseId, setCaseId] = useState<string>(CASES[0].id);

  const active = CASES.find((c) => c.id === caseId) ?? CASES[0];
  const outcome: ForgeOutcome<ForgeEnvStatusReport> = {
    kind: "report",
    meta: META,
    report: active.report,
  };

  function applyScheme(next: string) {
    setScheme(next);
    document.documentElement.setAttribute("data-color-scheme", next);
  }

  function applyDark(next: boolean) {
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
  }

  /*
   * Rendered inside the REAL shell (ForgeShell — the same component
   * ForgeLayout renders), not bare.
   *
   * A harness that drew the screen without the sidebar looked exactly like the
   * product to anyone reading a screenshot, and was not. That cost two review
   * cycles here, both spent asking where the left nav had gone when it was
   * present in the app the whole time. Rendering the real chrome makes that
   * mistake impossible rather than merely discouraged.
   */
  return (
    <ForgeShell
      activePath="/forge/environments"
      headerContent={
        <div className="flex flex-wrap items-center gap-3 px-4">
        <select
          data-testid="preview-case"
          value={caseId}
          onChange={(e) => setCaseId(e.target.value)}
          className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
        >
          {CASES.map((c) => (
            <option key={c.id} value={c.id}>
              {c.label}
            </option>
          ))}
        </select>
        <select
          data-testid="preview-scheme"
          value={scheme}
          onChange={(e) => applyScheme(e.target.value)}
          className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
        >
          {SCHEMES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <button
          type="button"
          data-testid="preview-dark"
          onClick={() => applyDark(!dark)}
          className="rounded-md border border-border bg-card px-3 py-1 text-sm text-foreground"
        >
          {dark ? "Dark" : "Light"}
        </button>
        </div>
      }
    >
      <div className="h-full overflow-y-auto p-6">
        <WorkloadInventory
          outcome={outcome}
          isLoading={false}
          env={active.env}
          projectName="control-plane"
        />
      </div>
    </ForgeShell>
  );
}
