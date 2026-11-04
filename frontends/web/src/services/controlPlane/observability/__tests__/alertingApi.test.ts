// Copyright (c) 2025 Reliant Labs

/**
 * The AlertingService wire mapping: what alertingApi.ts sends control-plane
 * and how it reads the answer back, plus how alerting's deliberate answers
 * ("not configured", "still routed") are told apart from outages.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { ReceiverType } from "@/gen/controlplane/services/alerting/v1/alerting_pb";

import {
  createReceiver,
  createSilence,
  listAlertHistory,
  listAlerts,
  listNotifications,
  listReceivers,
  listSilences,
  markNotificationsRead,
  testReceiver,
  updateReceiver,
} from "../alertingApi";
import { msOf } from "../api";
import { alertingErrorKind, alertingRetry } from "../errors";

const client = {
  listReceivers: vi.fn(),
  createReceiver: vi.fn(),
  updateReceiver: vi.fn(),
  testReceiver: vi.fn(),
  listSilences: vi.fn(),
  createSilence: vi.fn(),
  listAlerts: vi.fn(),
  listAlertHistory: vi.fn(),
  listNotifications: vi.fn(),
  markNotificationsRead: vi.fn(),
};

vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

const T = Date.UTC(2026, 9, 12, 6, 30);
const ts = (ms: number) => create(TimestampSchema, { seconds: BigInt(Math.floor(ms / 1000)), nanos: (ms % 1000) * 1e6 });

beforeEach(() => vi.clearAllMocks());

describe("receivers", () => {
  it("maps a receiver, never a secret, and an unknown type as unknown", async () => {
    client.listReceivers.mockResolvedValue({
      receivers: [
        { id: "r1", name: "oncall-slack", type: ReceiverType.SLACK, settings: { channel: "#oncall" }, hasSecret: true, createdAt: ts(T), updatedAt: ts(T + 1000) },
        { id: "r2", name: "reliant-inapp", type: ReceiverType.RELIANT_INAPP, settings: {}, hasSecret: false, createdAt: ts(T), updatedAt: ts(T) },
        { id: "r3", name: "future", type: 99, settings: {}, hasSecret: false, createdAt: ts(T), updatedAt: ts(T) },
      ],
    });
    const out = await listReceivers("org-1");
    expect(client.listReceivers).toHaveBeenCalledWith({ orgId: "org-1" });
    expect(out[0]).toEqual({ id: "r1", name: "oncall-slack", type: "slack", settings: { channel: "#oncall" }, hasSecret: true, createdAtMs: T, updatedAtMs: T + 1000 });
    expect(out.map((r) => r.type)).toEqual(["slack", "inapp", "unknown"]);
  });

  it("sends the type as the wire enum and the secret only on the way in", async () => {
    client.createReceiver.mockResolvedValue({ receiver: { id: "r9", name: "pd", type: ReceiverType.PAGERDUTY, settings: {}, hasSecret: true, createdAt: ts(T), updatedAt: ts(T) } });
    const made = await createReceiver("org-1", { name: "pd", type: "pagerduty", settings: {}, secret: "routing-key" });
    expect(client.createReceiver).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-1", name: "pd", type: ReceiverType.PAGERDUTY, secret: "routing-key" }));
    expect(made).toMatchObject({ type: "pagerduty", hasSecret: true });
    expect(made).not.toHaveProperty("secret");
  });

  it("replaces settings only when asked, and keeps the stored secret when none is given", async () => {
    client.updateReceiver.mockResolvedValue({ receiver: { id: "r1", name: "x", type: ReceiverType.EMAIL, settings: {}, hasSecret: false, createdAt: ts(T), updatedAt: ts(T) } });
    await updateReceiver("org-1", { id: "r1", secret: "new" });
    expect(client.updateReceiver.mock.calls[0][0]).toMatchObject({ id: "r1", setSettings: false, secret: "new" });
    await updateReceiver("org-1", { id: "r1", settings: { to: "a@example.com" } });
    expect(client.updateReceiver.mock.calls[1][0]).toMatchObject({ setSettings: true, settings: { to: "a@example.com" }, secret: "" });
  });

  it("reads a test's two answers apart: accepted, and live in Alertmanager", async () => {
    client.testReceiver.mockResolvedValue({ accepted: true, receiverLive: false });
    expect(await testReceiver("org-1", "r1")).toEqual({ accepted: true, receiverLive: false });
  });
});

describe("silences", () => {
  it("sends matchers and the end as a timestamp, starting now when no start is given", async () => {
    client.createSilence.mockResolvedValue({
      silence: { id: "s1", matchers: [{ name: "alertname", value: "X", isRegex: false, isEqual: true }], startsAt: ts(T), endsAt: ts(T + 3_600_000), createdBy: "a", comment: "c", state: "active" },
    });
    const made = await createSilence("org-1", { matchers: [{ name: "alertname", value: "X", isRegex: false, isEqual: true }], endsAtMs: T + 3_600_000, comment: "c" });
    const req = client.createSilence.mock.calls[0][0];
    expect(req.startsAt).toBeUndefined();
    expect(msOf(req.endsAt)).toBe(T + 3_600_000);
    expect(made).toMatchObject({ id: "s1", state: "active", startsAtMs: T, endsAtMs: T + 3_600_000 });
  });

  it("asks for expired ones only when wanted, and reads an unknown state as expired", async () => {
    client.listSilences.mockResolvedValue({ silences: [{ id: "s1", matchers: [], startsAt: ts(T), endsAt: ts(T), createdBy: "", comment: "", state: "weird" }] });
    const out = await listSilences("org-1", false);
    expect(client.listSilences.mock.calls[0][0]).toEqual({ orgId: "org-1", includeExpired: undefined });
    expect(out[0].state).toBe("expired");
  });
});

describe("alerts and history", () => {
  it("maps an instance, with absent times as null", async () => {
    client.listAlerts.mockResolvedValue({
      alerts: [{ envId: "env-1", rule: "R", fingerprint: "f", labels: { a: "1" }, annotations: { summary: "s" }, state: "pending", activeAt: ts(T), firingAt: undefined, lastEvalAt: ts(T), lastValue: 0.5 }],
    });
    const [alert] = await listAlerts("org-1", "env-1");
    expect(client.listAlerts).toHaveBeenCalledWith({ orgId: "org-1", envId: "env-1" });
    expect(alert).toEqual({ envId: "env-1", rule: "R", fingerprint: "f", labels: { a: "1" }, annotations: { summary: "s" }, state: "pending", activeAtMs: T, firingAtMs: null, lastEvalAtMs: T, lastValue: 0.5 });
  });

  it("sends since as a timestamp and reads an unknown transition as inactive", async () => {
    client.listAlertHistory.mockResolvedValue({ events: [{ envId: "e", rule: "R", fingerprint: "f", labels: {}, transition: "exploded", value: 1, at: ts(T) }] });
    const [event] = await listAlertHistory("org-1", { sinceMs: T - 3_600_000, limit: 50 });
    expect(msOf(client.listAlertHistory.mock.calls[0][0].since)).toBe(T - 3_600_000);
    expect(client.listAlertHistory.mock.calls[0][0].limit).toBe(50);
    expect(event).toMatchObject({ transition: "inactive", atMs: T });
  });
});

describe("notifications", () => {
  it("maps the list with its unread count, and marks every unread one when given no ids", async () => {
    client.listNotifications.mockResolvedValue({
      unread: 1,
      notifications: [{ id: "n1", envId: "e", status: "firing", alertname: "R", severity: "critical", labels: {}, annotations: { summary: "s" }, startsAt: ts(T), receivedAt: ts(T + 5000), read: false }],
    });
    const list = await listNotifications("org-1");
    expect(list.unread).toBe(1);
    expect(list.notifications[0]).toMatchObject({ id: "n1", status: "firing", severity: "critical", startsAtMs: T, receivedAtMs: T + 5000, read: false });
    client.markNotificationsRead.mockResolvedValue({ marked: 1 });
    expect(await markNotificationsRead("org-1")).toBe(1);
    expect(client.markNotificationsRead).toHaveBeenCalledWith({ orgId: "org-1", ids: [] });
  });
});

describe("alerting's answers", () => {
  it("tells 'not configured' and 'still routed' from an outage, and retries only the outage", () => {
    const notConfigured = new ConnectError("alerting is not configured on this server", Code.Unavailable, new Headers({ "x-forge-error-reason": "observability_not_configured" }));
    const inUse = new ConnectError("a route still names it", Code.FailedPrecondition);
    const outage = new ConnectError("upstream connect error", Code.Unavailable);
    expect(alertingErrorKind(notConfigured)).toBe("not-configured");
    expect(alertingErrorKind(inUse)).toBe("in-use");
    expect(alertingErrorKind(outage)).toBe("unavailable");
    expect(alertingErrorKind(new ConnectError("no", Code.PermissionDenied))).toBe("denied");
    expect(alertingRetry(0, notConfigured)).toBe(false);
    expect(alertingRetry(0, inUse)).toBe(false);
    expect(alertingRetry(0, outage)).toBe(true);
    expect(alertingRetry(1, outage)).toBe(false);
  });
});
