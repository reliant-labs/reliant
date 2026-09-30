// Copyright (c) 2025 Reliant Labs

/**
 * The domain data layer's two jobs: decode the control plane's vocabulary
 * without widening it, and classify a failure into something the screen can
 * describe rather than an error it must report.
 */

import { describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
}));

import {
  DeployCustomDomainState,
  DomainSource,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import {
  bindingSummary,
  domainAvailabilityFromError,
  domainIsConverging,
  domainOriginOf,
  domainStateOf,
  toForgeDomain,
  type DomainState,
} from "@/services/forge/domains";

describe("state decoding", () => {
  it.each([
    [DeployCustomDomainState.PENDING_DNS, "pending-dns"],
    [DeployCustomDomainState.VERIFYING, "verifying"],
    [DeployCustomDomainState.ISSUING, "issuing"],
    [DeployCustomDomainState.LIVE, "live"],
    [DeployCustomDomainState.FAILED, "failed"],
    [DeployCustomDomainState.CONFLICT, "conflict"],
    [DeployCustomDomainState.UNSPECIFIED, "unknown"],
  ] as const)("%s decodes to %s", (wire, expected) => {
    expect(domainStateOf(wire)).toBe(expected);
  });

  it("decodes a state this build does not know as `unknown`, never as pending-dns", () => {
    // A newer control plane adding a seventh state must not have it read as
    // "go and publish DNS" — that would be an instruction we invented.
    expect(domainStateOf(99 as DeployCustomDomainState)).toBe("unknown");
  });

  it("decodes source, defaulting unknown rather than toward platform", () => {
    expect(domainOriginOf(DomainSource.EXTERNAL)).toBe("external");
    expect(domainOriginOf(DomainSource.PLATFORM)).toBe("platform");
    expect(domainOriginOf(0 as DomainSource)).toBe("unknown");
  });
});

describe("domainIsConverging", () => {
  it("is true only while the platform can still move it on its own", () => {
    const converging: DomainState[] = ["pending-dns", "verifying", "issuing"];
    for (const state of converging) expect(domainIsConverging(state)).toBe(true);
  });

  it("is false for live, failed, conflict and unknown", () => {
    // `live` is false so the poll stops on a settled page. A live domain CAN
    // still regress (a deleted TXT), and the list picks that up on its next
    // ordinary read rather than by polling forever.
    for (const state of ["live", "failed", "conflict", "unknown"] as DomainState[]) {
      expect(domainIsConverging(state)).toBe(false);
    }
  });
});

describe("availability classification", () => {
  it("treats a denied read as a role fact, not a fault", () => {
    expect(domainAvailabilityFromError(new ConnectError("nope", Code.PermissionDenied))).toBe(
      "no-access"
    );
  });

  it("treats Unimplemented and Unavailable as `this control plane has no registry`", () => {
    expect(domainAvailabilityFromError(new ConnectError("x", Code.Unimplemented))).toBe(
      "not-configured"
    );
    expect(domainAvailabilityFromError(new ConnectError("x", Code.Unavailable))).toBe(
      "not-configured"
    );
  });

  it("only anything else is genuinely bad", () => {
    expect(domainAvailabilityFromError(new ConnectError("boom", Code.Internal))).toBe(
      "unreachable"
    );
  });
});

describe("conversion", () => {
  it("reads records verbatim and carries the binding through", () => {
    const converted = toForgeDomain({
      id: "dom-1",
      hostname: "hounders.club",
      state: DeployCustomDomainState.PENDING_DNS,
      source: DomainSource.EXTERNAL,
      requiredRecords: [
        { type: "A", name: "hounders.club", value: "34.63.203.181" },
        { type: "TXT", name: "_reliant-challenge.hounders.club", value: "tok" },
      ],
      lastError: "",
      binding: {
        id: "bind-1",
        domainId: "dom-1",
        environmentId: "env-prod",
        target: "web",
        redirectTo: "",
      },
    } as any);

    expect(converted.state).toBe("pending-dns");
    expect(converted.requiredRecords).toEqual([
      { type: "A", name: "hounders.club", value: "34.63.203.181" },
      { type: "TXT", name: "_reliant-challenge.hounders.club", value: "tok" },
    ]);
    expect(converted.binding?.target).toBe("web");
  });

  it("an absent binding is null, not an empty object", () => {
    const converted = toForgeDomain({
      id: "dom-2",
      hostname: "parked.example",
      state: DeployCustomDomainState.PENDING_DNS,
      source: DomainSource.EXTERNAL,
      requiredRecords: [],
      lastError: "",
    } as any);
    expect(converted.binding).toBeNull();
  });
});

describe("bindingSummary", () => {
  it("distinguishes unbound, bound and redirecting", () => {
    expect(bindingSummary(null)).toMatch(/not serving/i);
    expect(
      bindingSummary({
        id: "b",
        domainId: "d",
        environmentId: "e",
        target: "web",
        redirectTo: "",
      })
    ).toBe("web");
    expect(
      bindingSummary({
        id: "b",
        domainId: "d",
        environmentId: "e",
        target: "",
        redirectTo: "hounders.club",
      })
    ).toBe("Redirects to hounders.club");
  });
});
