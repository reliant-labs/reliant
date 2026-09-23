// The ONE seam between the deploy usage surface and the control plane.
//
// ⚠️ THIS SURFACE IS NOT YET REACHABLE, AND THE REASON IS A CODEGEN GAP.
//
// The four RPCs it needs are:
//
//   BillingService.GetCurrentUserInfraOverage   consumption, allowance, cap, rung
//   BillingService.SetCurrentUserInfraOverage   the ceiling write
//   DeployService.ListUsage                     attribution rows
//   DeployService.ListDeployments               id → name for those rows
//
// All four are MOUNTED on the listener this app reaches — `publicAPIMounts()`
// in control-plane's internal/app/deployments.go carries both MountSvcBilling
// and MountDeploy — so the server side is ready. What is missing is the
// generated TypeScript CLIENT.
//
// reliant/web's control-plane client is generated from
// `reliant/proto/controlplane/v1/public/` (buf.gen.controlplane.yaml), which
// is the CANONICAL home of the public controlplane.v1 API — control-plane
// consumes it from here, not the other way round. That subtree currently
// declares neither the Infra* messages nor DeployService at all, so
// `billing_service_pb.ts` has no `getCurrentUserInfraOverage` method and there
// is no `deploy_service_pb.ts` to import. The console version compiled only
// because internal-console generates from control-plane's own
// `proto/services/**`, a different and non-public tree.
//
// WHAT UNBLOCKS IT, in full:
//
//   1. Add the infra overage pair + their messages to
//      proto/controlplane/v1/public/billing_service.proto, and the
//      InfraDimensionUsage message to that subtree's shared.proto.
//   2. Add proto/controlplane/v1/public/deploy_service.proto with ListUsage
//      and ListDeployments (plus DeployUsageRow / DeployResourceKind).
//   3. `npm run proto:generate:controlplane`.
//   4. Replace the body of useDeployUsage below with the real queries. The
//      shapes in ./usageModel are already structurally compatible with what
//      protoc-gen-es emits for those messages, so the adapter is assignment,
//      not translation.
//
// Until then `available` is FALSE and billing.tsx renders the Usage tab
// exactly as it does today. That is deliberate: a surface that renders
// "unavailable" to a paying customer is worse than one that is not there yet,
// and a half-wired tab is indistinguishable from a broken product — the same
// reason internal-console's /usage route was deleted rather than left to 404.
//
// The DOMAIN layer this would feed (./usageModel) is complete, tested and
// independent of codegen, so none of the rules above are waiting on any of
// this.

import { useMemo } from "react";

import { buildUsageSummary } from "./usageModel";

import type { OverageRequestFields, UsageSummary } from "./usageModel";

export interface DeployUsageState {
  /**
   * Whether the RPCs backing this surface can be called at all in this build.
   *
   * A BUILD fact, not a server read — it reflects whether the generated client
   * carries the methods, so it cannot flap and callers may branch on it
   * structurally.
   */
  available: boolean;
  summary: UsageSummary;
  isLoading: boolean;
  error: { message?: string } | null;
  refetch: () => void;
  saveCap: (fields: OverageRequestFields) => void;
  isSavingCap: boolean;
  deploymentNames: Map<string, string>;
}

export function useDeployUsage(): DeployUsageState {
  // An empty summary rather than an absent one: every consumer reads the same
  // shape whether or not the transport exists, so turning this on later
  // changes no call site.
  const summary = useMemo(() => buildUsageSummary({ rows: [] }), []);

  return {
    available: false,
    summary,
    isLoading: false,
    error: null,
    refetch: () => undefined,
    saveCap: () => undefined,
    isSavingCap: false,
    deploymentNames: new Map(),
  };
}
