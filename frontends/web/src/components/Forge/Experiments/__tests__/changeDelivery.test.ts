// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

/**
 * Whether a change's saved revision reached the environment's apps
 * (changeModel.ts deliveryStateOf), from GetChangesDeliveryStatus as the
 * wire reader hands it over (changesWire.ts deliveryStatusOf): the targets'
 * written revisions against the ENVIRONMENT's snapshot revision.
 */

import { describe, expect, it } from "vitest";

import type { ChangeDeliveryTarget, ChangesDeliveryStatus } from "@/services/controlPlane/changes/changesTypes";
import { deliveryStatusOf } from "@/services/controlPlane/changes/changesWire";

import { agoOf, deliveryStateOf } from "../changeModel";

const target = (overrides: Partial<ChangeDeliveryTarget> = {}): ChangeDeliveryTarget => ({
  kind: "static-site",
  name: "shop-web",
  location: "/_forge/changes.json",
  revision: 42,
  deliveredAt: new Date("2026-11-20T10:00:00Z"),
  ...overrides,
});

const status = (revision: number, targets: ChangeDeliveryTarget[]): ChangesDeliveryStatus => ({ revision, targets });

describe("a change's delivery state", () => {
  it("is Saved while nothing says where the revision went", () => {
    expect(deliveryStateOf(undefined)).toEqual({ kind: "saved" });
  });

  it("says no hosted app receives it when the environment has no targets", () => {
    expect(deliveryStateOf(status(42, []))).toEqual({ kind: "no-targets" });
  });

  it("waits while any target has an older snapshot than the environment renders", () => {
    expect(deliveryStateOf(status(42, [target(), target({ kind: "config-map", name: "env/reliant-forge-changes", revision: 41 })]))).toEqual({
      kind: "waiting",
      delivered: 1,
      targets: 2,
    });
  });

  it("is Delivered when every target has the current snapshot, as of the latest write", () => {
    const later = new Date("2026-11-20T10:05:00Z");
    expect(deliveryStateOf(status(42, [target(), target({ kind: "config-map", name: "env/reliant-forge-changes", revision: 43, deliveredAt: later })]))).toEqual({
      kind: "delivered",
      targets: 2,
      at: later,
    });
    expect(deliveryStateOf(status(42, [target({ deliveredAt: null })]))).toEqual({ kind: "delivered", targets: 1, at: null });
  });

  it("compares with the environment's snapshot revision, never a change's own", () => {
    // A target at revision 7 is behind an environment whose snapshot is at 42, whatever one change's revision is.
    expect(deliveryStateOf(status(42, [target({ revision: 7 })])).kind).toBe("waiting");
  });

  it("reads the wire's protojson: int64 as strings, kinds by name, timestamps as RFC 3339", () => {
    expect(
      deliveryStatusOf({
        revision: "42",
        etag: "\"abc\"",
        targets: [
          { kind: "CHANGE_DELIVERY_TARGET_KIND_STATIC_SITE", name: "shop-web", location: "/_forge/changes.json", revision: "42", deliveredAt: "2026-11-20T10:00:00Z", current: true },
          { kind: "CHANGE_DELIVERY_TARGET_KIND_CONFIG_MAP", name: "ns/reliant-forge-changes", revision: "41" },
          { kind: "CHANGE_DELIVERY_TARGET_KIND_SOMETHING_NEW" },
        ],
      })
    ).toEqual({
      revision: 42,
      targets: [
        target(),
        { kind: "config-map", name: "ns/reliant-forge-changes", location: "", revision: 41, deliveredAt: null },
        { kind: "unknown", name: "", location: "", revision: 0, deliveredAt: null },
      ],
    });
    expect(deliveryStatusOf({})).toEqual({ revision: 0, targets: [] });
  });

  it("says when coarsely", () => {
    const now = new Date("2026-11-20T12:00:00Z").getTime();
    expect(agoOf(new Date("2026-11-20T11:59:30Z"), now)).toBe("just now");
    expect(agoOf(new Date("2026-11-20T11:48:00Z"), now)).toBe("12m ago");
    expect(agoOf(null, now)).toBe("");
  });
});
