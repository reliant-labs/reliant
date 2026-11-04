/**
 * The reason contract (lib/errorReasons.ts), reader by reader.
 *
 * Every reader keys on the `x-forge-error-reason` reason and nothing else
 * (P9 R3w2 removed the message-marker and `x-reliant-reason` fallbacks). So
 * every reason from 13-p9-frontend-plan.md §7.1 is pinned through the reader
 * the UI actually calls:
 *
 *   reason   — an error built from metadata only, with a message that says
 *              nothing, is classified by its reason;
 *   prose    — the old marker text with no reason is NOT claimed: an old
 *              server degrades to a plain error (D3), never a wrong branch;
 *   other    — a different known reason is not claimed either.
 */
import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { ConnectClientError } from "@reliantlabs/forge-web-runtime";

import { classifyRpcFailure } from "@/api/rpcErrorPolicy";
import { WorkflowState } from "@/gen/reliant/v1/chat_pb";
import { isMissingGitCredentialError } from "@/components/Projects/RepoSelector";
import { isBackendModalError } from "@/components/Settings/cloud/billingUtils";
import { isStorageDenied } from "@/components/Settings/cloud/diskGrow";
import { isEntitlementDenial, isReasonedQuotaError } from "@/hooks/useOnboardingQueries";
import { alertingErrorKind, deliveryErrorKind } from "@/services/controlPlane/observability/errors";
import { classifyDeployAuthzError } from "@/services/forge/deployAuthz";
import { isStepUpRequired } from "@/services/forge/secretStore";
import { isAccountRequiredError } from "../accountRequired";
import { sendOnExistingChat } from "../chatSendRouting";
import { isDaemonConnectingError, isDaemonPendingError, isNoMachineError } from "../daemon-errors";
import { isAlreadyResumedError, isQuotaResumeError, resumeErrorNeedsUpgrade } from "../daemon-resume";
import { errorReason, errorScopes, hasErrorReason, type ErrorReason } from "../errorReasons";
import { isForgeAPIDisabled } from "../forgeApiDisabled";

/** An error the switched servers send: the reason, and a message that names nothing. */
function reasoned(reason: string, code: Code = Code.FailedPrecondition, extra: Record<string, string> = {}): ConnectError {
  return new ConnectError("refused", code, new Headers({ "x-forge-error-reason": reason, ...extra }));
}

/** An error from before the servers sent reasons: no reason, the old text. */
function prose(message: string, code: Code = Code.FailedPrecondition): ConnectError {
  return new ConnectError(message, code);
}

interface MarkerCase {
  name: string;
  reason: ErrorReason;
  code: Code;
  /** The error as a server from before the reason sent it. */
  legacy: unknown;
  read: (err: unknown) => boolean;
}

// Every §7.1 marker, with the reader that branches on it.
const MARKERS: MarkerCase[] = [
  // #1 machine connecting — isDaemonConnectingError covers all four.
  {
    name: "#1 daemon starting",
    reason: "daemon_starting",
    code: Code.Unavailable,
    legacy: prose("your machine is still starting: no daemon connected: daemon record exists but has not registered yet (still starting)", Code.Unavailable),
    read: isDaemonConnectingError,
  },
  {
    name: "#1 daemon suspended",
    reason: "daemon_suspended",
    code: Code.Unavailable,
    legacy: prose("no daemon connected: the machine is asleep (suspended)", Code.Unavailable),
    read: isDaemonConnectingError,
  },
  {
    name: "#1 daemon waking",
    reason: "daemon_waking",
    code: Code.Unavailable,
    legacy: prose("your machine is waking up: no daemon connected yet", Code.Unavailable),
    read: isDaemonConnectingError,
  },
  {
    name: "#1 daemon not connected",
    reason: "daemon_not_connected",
    code: Code.Internal,
    legacy: prose("unavailable: no daemon connected for user", Code.Internal),
    read: isDaemonConnectingError,
  },
  {
    name: "#1 daemon pending (starting or suspended)",
    reason: "daemon_suspended",
    code: Code.Unavailable,
    legacy: prose("the machine for this request is suspended and will wake: no daemon connected", Code.Unavailable),
    read: isDaemonPendingError,
  },
  // #2
  {
    name: "#2 no machine at all",
    reason: "no_daemon",
    code: Code.FailedPrecondition,
    legacy: prose("resolving daemon for command: no daemon available"),
    read: isNoMachineError,
  },
  // #4 (WorkflowBuilderPage reads hasErrorReason directly)
  {
    name: "#4 stale workflow revision",
    reason: "stale_revision",
    code: Code.Aborted,
    legacy: prose("workflow was modified since you loaded it", Code.Aborted),
    read: (e) => hasErrorReason(e, "stale_revision"),
  },
  // #5
  {
    name: "#5 resume of a machine that is not suspended",
    reason: "daemon_not_suspended",
    code: Code.FailedPrecondition,
    legacy: prose("daemon is not suspended"),
    read: isAlreadyResumedError,
  },
  // #6 — the reason was only ever sent under x-reliant-reason; there was no text arm.
  {
    name: "#6 resume refused: compute spent",
    reason: "compute_budget_exhausted",
    code: Code.ResourceExhausted,
    legacy: new ConnectError("refused", Code.ResourceExhausted, new Headers({ "x-reliant-reason": "compute_budget_exhausted" })),
    read: isQuotaResumeError,
  },
  // #7
  {
    name: "#7 deploy: permission missing",
    reason: "scope_missing",
    code: Code.PermissionDenied,
    legacy: new Error("plan failed: [permission_denied] your permissions do not include deploy:write"),
    read: (e) => classifyDeployAuthzError(e as Error)?.kind === "permission-denied",
  },
  {
    name: "#7 deploy: no control-plane credential",
    reason: "no_control_plane_credential",
    code: Code.Unauthenticated,
    legacy: new Error("no control-plane credential on this machine; run forge login"),
    read: (e) => classifyDeployAuthzError(e as Error)?.kind === "not-authorized",
  },
  // #8
  {
    name: "#8 alerting not configured",
    reason: "observability_not_configured",
    code: Code.Unavailable,
    legacy: prose("alerting is not configured on this server", Code.Unavailable),
    read: (e) => alertingErrorKind(e) === "not-configured",
  },
  {
    name: "#8 not an org member",
    reason: "not_org_member",
    code: Code.PermissionDenied,
    legacy: prose("not a member of this organization", Code.PermissionDenied),
    read: (e) => deliveryErrorKind(e) === "not-member",
  },
  // #9 (GitHubConnectStep holds the same check)
  {
    name: "#9 no git credential",
    reason: "git_credential_not_connected",
    code: Code.Unavailable,
    legacy: new Error("no git credential found for provider github"),
    read: isMissingGitCredentialError,
  },
  // #11 (CommandsViewerTab's kill reads hasErrorReason directly)
  {
    name: "#11 process already stopped",
    reason: "process_not_running",
    code: Code.FailedPrecondition,
    legacy: new Error("process p-1 is not running"),
    read: (e) => hasErrorReason(e, "process_not_running"),
  },
  // #12 — the readers of the retired x-reliant-reason header.
  {
    name: "#12 account required",
    reason: "account_required",
    code: Code.FailedPrecondition,
    legacy: new ConnectError("refused", Code.FailedPrecondition, new Headers({ "x-reliant-reason": "account_required" })),
    read: isAccountRequiredError,
  },
  {
    name: "#12 account required (rpc policy)",
    reason: "account_required",
    code: Code.Internal,
    legacy: new ConnectError("refused", Code.Internal, new Headers({ "x-reliant-reason": "account_required" })),
    read: (e) => classifyRpcFailure(e, { serviceTypeName: "controlplane.v1.DaemonService", signalAborted: false }) === "account-required",
  },
  {
    name: "#12 forge api disabled",
    reason: "forge_api_disabled",
    code: Code.FailedPrecondition,
    legacy: new ConnectError("refused", Code.FailedPrecondition, new Headers({ "x-reliant-reason": "forge_api_disabled" })),
    read: isForgeAPIDisabled,
  },
  {
    name: "#12 disk grow denied",
    reason: "daemon_storage_denied",
    code: Code.PermissionDenied,
    legacy: new ConnectError("refused", Code.PermissionDenied, new Headers({ "x-reliant-reason": "daemon_storage_denied" })),
    read: isStorageDenied,
  },
  {
    name: "#12 billing email missing (backend modal)",
    reason: "billing_email_missing",
    code: Code.InvalidArgument,
    legacy: new ConnectError("refused", Code.InvalidArgument, new Headers({ "x-reliant-reason": "billing_email_missing" })),
    read: isBackendModalError,
  },
  {
    name: "#12 quota (backend modal)",
    reason: "compute_budget_exhausted",
    code: Code.ResourceExhausted,
    legacy: new ConnectError("refused", Code.ResourceExhausted, new Headers({ "x-reliant-reason": "compute_budget_exhausted" })),
    read: isBackendModalError,
  },
  {
    name: "#12 reasoned quota (onboarding)",
    reason: "compute_budget_exhausted",
    code: Code.ResourceExhausted,
    legacy: new ConnectError("refused", Code.ResourceExhausted, new Headers({ "x-reliant-reason": "compute_budget_exhausted" })),
    read: isReasonedQuotaError,
  },
  {
    name: "#12 entitlement denial (onboarding)",
    reason: "daemon_size_denied",
    code: Code.PermissionDenied,
    legacy: new ConnectError("refused", Code.PermissionDenied, new Headers({ "x-reliant-reason": "daemon_size_denied" })),
    read: isEntitlementDenial,
  },
  {
    name: "#12 resume needs an upgrade",
    reason: "no_compute_subscription",
    code: Code.FailedPrecondition,
    legacy: new ConnectError("refused", Code.FailedPrecondition, new Headers({ "x-reliant-reason": "no_compute_subscription" })),
    read: resumeErrorNeedsUpgrade,
  },
  {
    name: "step-up required",
    reason: "step_up_required",
    code: Code.FailedPrecondition,
    legacy: prose("a fresh step-up is required for this environment"),
    read: isStepUpRequired,
  },
];

// A known reason that none of the readers above asks for.
const UNRELATED: ErrorReason = "chat_not_started";

describe.each(MARKERS)("$name", ({ reason, code, legacy: legacyError, read }) => {
  it("reason: is read from x-forge-error-reason, with nothing in the message", () => {
    expect(read(reasoned(reason, code))).toBe(true);
  });

  it("reason: is read from a ConnectClientError's .reason (the runtime chain)", () => {
    expect(read(new ConnectClientError({ message: "refused", code, reason }))).toBe(true);
  });

  it("prose: the old marker (or the retired x-reliant-reason header) alone is not claimed", () => {
    expect(read(legacyError)).toBe(false);
  });

  it("other: a different known reason is not claimed", () => {
    expect(read(reasoned(UNRELATED, code))).toBe(false);
  });

  it("nothing: an unrelated error is not claimed", () => {
    // Internal, because some markers are code-shaped (a "[permission_denied]"
    // message is the deploy panel's permission marker).
    expect(read(new ConnectError("something else broke", Code.Internal))).toBe(false);
  });
});

describe("#3 chat not started routes a send to StartChat", () => {
  async function routed(error: unknown): Promise<boolean> {
    const actions = {
      startExistingChat: async () => ({}),
      sendMessage: async () => {
        throw error;
      },
    };
    try {
      await sendOnExistingChat({ workflowState: WorkflowState.ACTIVE }, "c-1", "hi", undefined, { workflow: null }, actions);
      return true;
    } catch {
      return false;
    }
  }

  it("reason", async () => {
    expect(await routed(reasoned("chat_not_started"))).toBe(true);
  });
  it("prose: the old text without the reason is not enough", async () => {
    expect(await routed(prose("chat has not started"))).toBe(false);
  });
  it("other: a different known reason does not route", async () => {
    expect(
      await routed(
        new ConnectError("chat has not started", Code.FailedPrecondition, new Headers({ "x-forge-error-reason": "stale_revision" })),
      ),
    ).toBe(false);
  });
});

describe("errorReason", () => {
  it("reads x-forge-error-reason, else null", () => {
    expect(errorReason(reasoned("daemon_waking"))).toBe("daemon_waking");
    expect(errorReason(new ConnectError("refused", Code.FailedPrecondition, new Headers({ "x-reliant-reason": "account_required" })))).toBeNull();
    expect(errorReason(prose("no reason here"))).toBeNull();
    expect(errorReason(new Error("not an rpc error"))).toBeNull();
    expect(errorReason(undefined)).toBeNull();
  });

  it("forwards a reason this web has no branch for (the upgrade modal's copy table)", () => {
    expect(errorReason(reasoned("some_future_reason"))).toBe("some_future_reason");
  });
});

describe("entitlement readers never mean 'any reason at all'", () => {
  // Once the servers stamp reasons everywhere, a coupon's ResourceExhausted
  // carries one too; it must not open the upgrade modal.
  const coupon = reasoned("coupon_exhausted", Code.ResourceExhausted);
  it("a coupon refusal is not a backend modal, a reasoned quota, or an entitlement denial", () => {
    expect(isBackendModalError(coupon)).toBe(false);
    expect(isReasonedQuotaError(coupon)).toBe(false);
    expect(isEntitlementDenial(coupon)).toBe(false);
    expect(resumeErrorNeedsUpgrade(coupon)).toBe(false);
  });
});

describe("errorScopes", () => {
  it("reads the structured header", () => {
    const err = reasoned("scope_missing", Code.PermissionDenied, { "x-forge-error-scopes": "deploy:write, secret:read" });
    expect(errorScopes(err)).toEqual(["deploy:write", "secret:read"]);
    expect(classifyDeployAuthzError(err)).toEqual({ kind: "permission-denied", permission: "deploy:write, secret:read" });
  });

  it("never reads a scope out of the prose", () => {
    const err = new Error("your permissions do not include deploy:write");
    expect(errorScopes(err)).toEqual([]);
    expect(classifyDeployAuthzError(err)).toBeNull();
  });

  it("is empty when nothing names one", () => {
    expect(errorScopes(reasoned("scope_missing", Code.PermissionDenied))).toEqual([]);
  });
});
