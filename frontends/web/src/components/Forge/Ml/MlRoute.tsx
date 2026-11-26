// Copyright (c) 2026 Reliant Labs

/**
 * /forge/ml/$view (org form /o/<org>/ml/$view): the ML tab, behind its gate.
 *
 * THE GATE. Every ML route first reads the org's entitlements
 * (OrganizationService.GetMyOrgEntitlements, cached for a minute). Without
 * `ml` enabled — or as soon as any ML read answers FEATURE_NOT_ENTITLED,
 * after a revocation — the route renders the one access page (MlAccessPage),
 * never an error. The server decides; this only predicts it.
 *
 * A control plane that has no GetMyOrgEntitlements yet (Unimplemented) has
 * no ML either: the access page, whose request then falls back to email.
 *
 * In the demo shell the fixture pack answers every call, `ml` enabled
 * included, so the pages render with no branch on the shell here.
 *
 * MOUNTABLE ANYWHERE. Each view (./<Area>/Ml<Area>View.tsx) is a
 * self-contained route component: it takes the drill-down segments under its
 * path (`sub`), reads its own data through the gate's org (kit/useMl) and its
 * own state from the URL search (kit/links useMlSearch). So a view can be
 * mounted under another area of the console (Deployments, Observability,
 * Inbox) by wrapping it in <MlGatedView>, with no change to the view.
 */

import { useCallback, useMemo, useState } from "react";
import { useParams } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Cpu, RefreshCw } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import { mlKeys, useMlEntitlement, useMlOrg } from "@/hooks/ml-queries";
import { mlErrorKind } from "@/services/controlPlane/ml/errors";
import { obsErrorMessage } from "@/services/controlPlane/observability/errors";

import { isMlView, ML_VIEWS, type MlView } from "../Console/paths";
import { ObsLoadingRows, StateFrame } from "../Observability/ObsStates";
import { MlAccessPage } from "./MlAccessPage";
import { MlGateContext, type MlGate } from "./MlParts";
import { MlEvalsView } from "./Evals/MlEvalsView";
import { MlFleetView } from "./Fleet/MlFleetView";
import { MlInferenceView } from "./Inference/MlInferenceView";
import { MlModelsView } from "./Models/MlModelsView";
import { MlOverviewView } from "./Overview/MlOverviewView";
import { MlQueuesView } from "./Queues/MlQueuesView";
import { MlRecommendationsView } from "./Recommendations/MlRecommendationsView";
import { MlSetupView } from "./Setup/MlSetupView";
import { MlTrainingView } from "./Training/MlTrainingView";

export function MlRoute() {
  const params = useParams({ strict: false }) as { view?: string; _splat?: string };
  const view: MlView = isMlView(params.view) ? params.view : ML_VIEWS[0];
  const sub = useMemo(() => (params._splat ?? "").split("/").filter(Boolean).map(safeDecode), [params._splat]);
  return <MlGatedView view={view} sub={sub} />;
}

function safeDecode(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

export interface MlGatedViewProps {
  view: MlView;
  /** The drill-down under the view's path: `["llama-8b-support-v3", "phases", "sft"]`. */
  sub?: readonly string[];
}

/** The gate and the page, free of the router: what tests and the preview render. */
export function MlGatedView({ view, sub = NO_SUB }: MlGatedViewProps) {
  const org = useMlOrg();
  const orgId = org.orgId;
  const entitlement = useMlEntitlement(orgId);
  const qc = useQueryClient();
  // An ML read refused for lack of the entitlement since the gate read it.
  const [refused, setRefused] = useState(false);
  const reportNotEntitled = useCallback(() => {
    setRefused(true);
    if (orgId) void qc.invalidateQueries({ queryKey: mlKeys.entitlements(orgId) });
  }, [orgId, qc]);
  const gate = useMemo<MlGate | null>(() => (orgId ? { orgId, reportNotEntitled } : null), [orgId, reportNotEntitled]);

  if (org.isLoading || (orgId && entitlement.isLoading)) {
    return (
      <Card padding="none" data-testid="ml-gate-loading">
        <ObsLoadingRows rows={4} testId="ml-gate-loading-rows" />
      </Card>
    );
  }
  if (!orgId || !gate) {
    return (
      <StateFrame testId="ml-no-org" tone={org.error ? "error" : "neutral"} icon={<Cpu className="h-4 w-4" />} title="No organization to show ML for">
        <p>{org.error ? obsErrorMessage(org.error) : "Sign in to an organization to see its ML workloads."}</p>
      </StateFrame>
    );
  }
  // Unimplemented: a control plane with no entitlements has no ML either.
  const unreadable = entitlement.error && mlErrorKind(entitlement.error) !== "no-data" ? entitlement.error : null;
  if (unreadable) {
    return (
      <StateFrame
        testId="ml-gate-error"
        tone="error"
        icon={<Cpu className="h-4 w-4" />}
        title="Couldn't check this organization's access to ML"
        actions={
          <Button variant="outline" size="sm" leftIcon={<RefreshCw className="h-3.5 w-3.5" />} onClick={() => void entitlement.refetch()} disabled={entitlement.isFetching}>
            Retry
          </Button>
        }
      >
        <p className="break-words font-mono text-meta">{obsErrorMessage(unreadable)}</p>
      </StateFrame>
    );
  }
  const ml = entitlement.data ?? null;
  if (refused || !ml?.enabled) {
    return <MlAccessPage orgId={orgId} entitlement={ml} view={view} source={refused ? "refused" : "entitlement"} />;
  }
  return (
    <MlGateContext.Provider value={gate}>
      <MlPage view={view} sub={sub} />
    </MlGateContext.Provider>
  );
}

const NO_SUB: readonly string[] = [];

/** The view for `view`, given its drill-down. */
export function MlPage({ view, sub = NO_SUB }: MlGatedViewProps) {
  switch (view) {
    case "overview":
      return <MlOverviewView sub={sub} />;
    case "runs":
      return <MlTrainingView sub={sub} />;
    case "evals":
      return <MlEvalsView sub={sub} />;
    case "models":
      return <MlModelsView sub={sub} />;
    case "inference":
      return <MlInferenceView sub={sub} />;
    case "fleet":
      return <MlFleetView sub={sub} />;
    case "queues":
      return <MlQueuesView sub={sub} />;
    case "recommendations":
      return <MlRecommendationsView sub={sub} />;
    case "setup":
      return <MlSetupView sub={sub} />;
  }
}
