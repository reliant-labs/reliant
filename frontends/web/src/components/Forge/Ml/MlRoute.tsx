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
 * A refused read latches the access page for ITS org only, until the
 * entitlement changes: a re-read that says disabled (the cache agrees now,
 * so a later grant opens the pages), a new grant, or a request for access
 * the server answers "already enabled". A re-read that still says enabled
 * under the same grant keeps the latch, so a server whose entitlement and
 * ML reads disagree shows the access page instead of re-reading in a loop.
 *
 * A control plane that has no GetMyOrgEntitlements yet (Unimplemented) has
 * no ML either: the access page, whose request then falls back to email.
 *
 * In the demo shell the fixture pack answers every call, `ml` enabled
 * included, so the six pages render with no branch on the shell here.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Cpu, RefreshCw } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import { mlKeys, useMlEntitlement, useMlOrg } from "@/hooks/ml-queries";
import { mlErrorKind } from "@/services/controlPlane/ml/errors";
import type { OrgEntitlement } from "@/services/controlPlane/ml/types";
import { obsErrorMessage } from "@/services/controlPlane/observability/errors";

import { isMlView, ML_VIEWS, type MlView } from "../Console/paths";
import { ObsLoadingRows, StateFrame } from "../Observability/ObsStates";
import { MlAccessPage } from "./MlAccessPage";
import { MlGateContext, type MlGate } from "./MlParts";
import { MlInferencePage } from "./MlInferencePage";
import { MlMachinesPage } from "./MlMachinesPage";
import { MlModelsPage } from "./MlModelsPage";
import { MlRecommendationsPage } from "./MlRecommendationsPage";
import { MlRunsPage } from "./MlRunsPage";
import { MlStackPage } from "./MlStackPage";

export function MlRoute() {
  const params = useParams({ strict: false }) as { view?: string };
  const view: MlView = isMlView(params.view) ? params.view : ML_VIEWS[0];
  const search = useSearch({ strict: false }) as { app?: string };
  const navigate = useNavigate();
  const onApp = useCallback(
    (app: string | undefined) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, app }), replace: true } as never),
    [navigate],
  );
  return <MlGatedView view={view} app={search.app ?? ""} onApp={onApp} />;
}

export interface MlGatedViewProps {
  view: MlView;
  /** The app Runs, Models and Inference narrow to; "" for every app. */
  app: string;
  onApp: (app: string | undefined) => void;
}

/** The gate and the page, free of the router: what tests and the preview render. */
export function MlGatedView({ view, app, onApp }: MlGatedViewProps) {
  const org = useMlOrg();
  const orgId = org.orgId;
  const entitlement = useMlEntitlement(orgId);
  const qc = useQueryClient();
  const ml = entitlement.data ?? null;
  // An ML read refused for lack of the entitlement since the gate read it:
  // the org it was refused for, and the grant the gate believed in then.
  const [refusal, setRefusal] = useState<MlRefusal | null>(null);
  const grantedAtMs = ml?.grantedAtMs ?? null;
  const reportNotEntitled = useCallback(() => {
    if (!orgId) return;
    setRefusal((prev) => (prev?.orgId === orgId ? prev : { orgId, grantedAtMs }));
    void qc.invalidateQueries({ queryKey: mlKeys.entitlements(orgId) });
  }, [orgId, grantedAtMs, qc]);
  const recover = useCallback(
    (id: string) => {
      setRefusal(null);
      // A refused read's cached answer would refuse again the moment its page reopens.
      void qc.resetQueries({ queryKey: mlKeys.all(id), predicate: (q) => q.queryKey[2] !== "entitlements" });
    },
    [qc],
  );
  useEffect(() => {
    if (!refusal) return;
    // Another org: the latch was never about it (its reads were dropped with the switch).
    if (refusal.orgId !== orgId) setRefusal(null);
    else if (ml && refusalSettled(refusal, ml)) recover(refusal.orgId);
  }, [refusal, orgId, ml, recover]);
  const refused = refusal !== null && refusal.orgId === orgId;
  const onAlreadyEnabled = useCallback(() => {
    if (orgId) recover(orgId);
  }, [orgId, recover]);
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
  if (refused || !ml?.enabled) {
    // Keyed by org: a request made for one org never shows, or disables the button, for another.
    return <MlAccessPage key={orgId} orgId={orgId} entitlement={ml} view={view} source={refused ? "refused" : "entitlement"} onAlreadyEnabled={onAlreadyEnabled} />;
  }
  return (
    <MlGateContext.Provider value={gate}>
      <MlPage view={view} app={app} onApp={onApp} />
    </MlGateContext.Provider>
  );
}

interface MlRefusal {
  orgId: string;
  /** The grant the entitlement reported when the read was refused. */
  grantedAtMs: number | null;
}

/** The entitlement has changed since the refusal: disabled now (the cache agrees), or granted anew. */
function refusalSettled(refusal: MlRefusal, ml: OrgEntitlement): boolean {
  return !ml.enabled || ml.grantedAtMs !== refusal.grantedAtMs;
}

function MlPage({ view, app, onApp }: MlGatedViewProps) {
  switch (view) {
    case "machines":
      return <MlMachinesPage />;
    case "stack":
      return <MlStackPage />;
    case "recommendations":
      return <MlRecommendationsPage />;
    case "runs":
      return <MlRunsPage app={app} onApp={onApp} />;
    case "models":
      return <MlModelsPage app={app} onApp={onApp} />;
    case "inference":
      return <MlInferencePage app={app} onApp={onApp} />;
  }
}
