// Copyright (c) 2026 Reliant Labs

/**
 * The ListFrontendReplay wire mapping: the scope → request (an unset project
 * stays unset, so the server reads every project), enum values → tokens (one
 * this build does not know is "unknown", never a state it would assert), the
 * sample rate held to [0, 1], and the environments the server could not read
 * passed through. It takes no read slot: a Postgres read never queues behind
 * ClickHouse.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";

import {
  FrontendReplaySchema,
  FrontendReplaySource,
  FrontendReplayState,
} from "@/gen/controlplane/services/observability/v1/observability_pb";

import { listFrontendReplay } from "../frontendReplayApi";
import { NO_READ_SLOT_RPCS, READ_SLOT_RPCS } from "../readSlots";

const client = { listFrontendReplay: vi.fn() };
vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));

beforeEach(() => vi.clearAllMocks());

const row = (over: MessageInitShape<typeof FrontendReplaySchema> = {}) =>
  create(FrontendReplaySchema, {
    envId: "env-prod",
    envName: "prod",
    frontend: "web",
    state: FrontendReplayState.ON,
    source: FrontendReplaySource.LIVE,
    sampleRate: 0.25,
    ...over,
  });

describe("listFrontendReplay", () => {
  it("sends the org, the project and the environments", async () => {
    client.listFrontendReplay.mockResolvedValue({ frontends: [], unavailableEnvIds: [] });
    await listFrontendReplay({ orgId: "org-1", projectId: "shop", envIds: ["env-prod", "env-staging"] });
    expect(client.listFrontendReplay.mock.calls[0][0]).toEqual({ orgId: "org-1", projectId: "shop", envIds: ["env-prod", "env-staging"] });
  });

  it("leaves the project unset when there is none", async () => {
    client.listFrontendReplay.mockResolvedValue({ frontends: [], unavailableEnvIds: [] });
    await listFrontendReplay({ orgId: "org-1", projectId: "", envIds: [] });
    expect(client.listFrontendReplay.mock.calls[0][0].projectId).toBeUndefined();
  });

  it("maps each state and source, and an unknown or unset one to unknown / none", async () => {
    client.listFrontendReplay.mockResolvedValue({
      frontends: [
        row({ state: FrontendReplayState.ON, source: FrontendReplaySource.LIVE }),
        row({ frontend: "admin", state: FrontendReplayState.OFF, source: FrontendReplaySource.RELEASE }),
        row({ frontend: "docs", state: FrontendReplayState.BLOCKED, source: FrontendReplaySource.RELEASE }),
        row({ frontend: "shop", state: FrontendReplayState.UNKNOWN, source: FrontendReplaySource.NONE }),
        row({ frontend: "old", state: FrontendReplayState.UNSPECIFIED, source: FrontendReplaySource.UNSPECIFIED }),
        row({ frontend: "new", state: 99 as FrontendReplayState, source: 99 as FrontendReplaySource }),
      ],
      unavailableEnvIds: ["env-dev"],
    });
    const list = await listFrontendReplay({ orgId: "org-1", envIds: [] });
    expect(list.frontends.map((f) => [f.frontend, f.state, f.source])).toEqual([
      ["web", "on", "live"],
      ["admin", "off", "release"],
      ["docs", "blocked", "release"],
      ["shop", "unknown", "none"],
      ["old", "unknown", "none"],
      ["new", "unknown", "none"],
    ]);
    expect(list.frontends[0]).toEqual({ envId: "env-prod", envName: "prod", frontend: "web", state: "on", source: "live", sampleRate: 0.25 });
    expect(list.unavailableEnvIds).toEqual(["env-dev"]);
  });

  it("holds the sample rate to [0, 1]", async () => {
    client.listFrontendReplay.mockResolvedValue({
      frontends: [row({ sampleRate: 4 }), row({ sampleRate: -1 }), row({ sampleRate: Number.NaN })],
      unavailableEnvIds: [],
    });
    const list = await listFrontendReplay({ orgId: "org-1", envIds: [] });
    expect(list.frontends.map((f) => f.sampleRate)).toEqual([1, 0, 1]);
  });

  it("takes no read slot", () => {
    expect(NO_READ_SLOT_RPCS.has("listFrontendReplay")).toBe(true);
    expect(READ_SLOT_RPCS.has("listFrontendReplay")).toBe(false);
  });
});
