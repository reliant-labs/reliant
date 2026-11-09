// Copyright (c) 2026 Reliant Labs

/**
 * replayStatus: the server's per-frontend verdict, reconciled with the replays
 * that arrived. "off" with replays is the build flag; "off" whose evidence
 * could not be read, or stopped before the end of the window, is unknown,
 * never asserted; an environment the
 * server could not read is unknown, never "off".
 */

import { describe, expect, it } from "vitest";

import type { FrontendReplay, FrontendReplayList } from "@/services/controlPlane/observability/frontendReplayApi";

import { offEnvIds, replayStatus, samplePercent, servesFrontend } from "../replayStatus";

const row = (over: Partial<FrontendReplay> = {}): FrontendReplay => ({
  envId: "env-prod",
  envName: "prod",
  frontend: "web",
  state: "on",
  source: "live",
  sampleRate: 1,
  ...over,
});
const list = (frontends: FrontendReplay[], unavailableEnvIds: string[] = []): FrontendReplayList => ({ frontends, unavailableEnvIds });
const all = (sessions: { envId: string; service: string }[] = []) => ({ sessions, complete: true });
const kinds = (l: FrontendReplayList, evidence: { sessions: { envId: string; service: string }[]; complete: boolean } | null = all()) =>
  replayStatus(l, evidence)?.frontends.map((f) => `${f.envName}/${f.frontend}:${f.kind}`);

describe("replayStatus", () => {
  it("passes on, off and blocked through when nothing contradicts them", () => {
    const l = list([row(), row({ frontend: "admin", state: "off" }), row({ frontend: "docs", state: "blocked" })]);
    expect(kinds(l)).toEqual(["prod/web:on", "prod/admin:off", "prod/docs:blocked"]);
    expect(replayStatus(l, all())!.hedged).toBe(false);
  });

  it("reads an off frontend whose replays arrived as on via the build flag", () => {
    const l = list([row({ state: "off" }), row({ envId: "env-staging", envName: "staging", state: "off" })]);
    expect(kinds(l, all([{ envId: "env-prod", service: "shop-web" }]))).toEqual(["prod/web:build-flag", "staging/web:off"]);
  });

  it("reads an off frontend with no replay found as unknown when the evidence read stopped early", () => {
    const l = list([row({ state: "off" }), row({ envId: "env-staging", envName: "staging", state: "off" })]);
    const status = replayStatus(l, { sessions: [{ envId: "env-prod", service: "shop-web" }], complete: false })!;
    // A replay it did find still counts; the absence of one proves nothing.
    expect(status.frontends.map((f) => `${f.envName}:${f.kind}`)).toEqual(["prod:build-flag", "staging:unknown"]);
    expect(status.hedged).toBe(true);
  });

  it("never asserts off when the evidence could not be read", () => {
    const l = list([row({ state: "off" }), row({ frontend: "admin" })]);
    const status = replayStatus(l, null)!;
    expect(status.frontends.map((f) => f.kind)).toEqual(["unknown", "on"]);
    expect(status.hedged).toBe(true);
  });

  it("keeps the generic how-to beside an unknown frontend", () => {
    const status = replayStatus(list([row({ state: "unknown", source: "none" }), row({ frontend: "admin", state: "off" })]), all())!;
    expect(status.frontends.map((f) => f.kind)).toEqual(["unknown", "off"]);
    expect(status.hedged).toBe(true);
  });

  it("names an unreadable environment, and treats it as unknown", () => {
    const status = replayStatus(list([row()], ["env-dev", "env-x"]), all(), (id) => (id === "env-dev" ? "dev" : undefined))!;
    expect(status.unavailableEnvs).toEqual([{ id: "env-dev", name: "dev" }, { id: "env-x", name: "env-x" }]);
    expect(status.hedged).toBe(true);
  });

  it("is null when there is nothing to say per frontend", () => {
    expect(replayStatus(list([]), all())).toBeNull();
  });

  it("gives a replay to the frontend with the longest matching name, in its own environment", () => {
    const l = list([
      row({ frontend: "web", state: "off" }),
      row({ frontend: "admin-web", state: "off" }),
      row({ envId: "env-staging", envName: "staging", frontend: "web", state: "off" }),
    ]);
    expect(kinds(l, all([{ envId: "env-prod", service: "shop-admin-web" }]))).toEqual(["prod/web:off", "prod/admin-web:build-flag", "staging/web:off"]);
  });

  it("falls back to the place's name, then the id, for a row the server did not name", () => {
    const status = replayStatus(list([row({ envName: "" }), row({ envId: "env-q", envName: "" })]), all(), (id) => (id === "env-prod" ? "prod" : undefined))!;
    expect(status.frontends.map((f) => f.envName)).toEqual(["prod", "env-q"]);
  });
});

describe("helpers", () => {
  it("matches a session's service to a frontend: <project>-<frontend>, or the bare name", () => {
    expect(servesFrontend("shop-web", "web")).toBe(true);
    expect(servesFrontend("web", "web")).toBe(true);
    expect(servesFrontend("shopweb", "web")).toBe(false);
    expect(servesFrontend("shop-web-2", "web")).toBe(false);
  });

  it("formats the sample rate as a percent", () => {
    expect([1, 0.25, 0.005, 0].map(samplePercent)).toEqual(["100%", "25%", "0.5%", "0%"]);
  });

  it("lists each environment with an off frontend once", () => {
    expect(offEnvIds(list([row({ state: "off" }), row({ frontend: "a", state: "off" }), row({ envId: "env-s" }), row({ envId: "env-t", state: "off" })]))).toEqual([
      "env-prod",
      "env-t",
    ]);
  });
});
