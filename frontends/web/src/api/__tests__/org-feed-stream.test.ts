/**
 * The org feed's client: it forwards rows, resumes from the cursor it has
 * processed, and stops (instead of retrying forever) when the server refuses
 * the caller, as it does for a removed member or an agent's credential.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  OrgStreamEventSchema,
  OrgUpdateBatchSchema,
  OrgUpdateSchema,
  OrgUpdateType,
  type OrgStreamEvent,
} from "@/gen/reliant/v1/streaming_pb";

vi.mock("@/lib/connect", () => ({ connectClient: vi.fn() }));
vi.mock("@/lib/pageActivity", () => ({ pageActivity: () => ({ noteConnectionLost: vi.fn() }) }));

const { OrgFeedStream, isOrgFeedRefusal } = await import("../org-feed-stream");

type Script = { events: OrgStreamEvent[]; then: "end" | "stay" | Error };

function fakeClient(scripts: Script[]) {
  const requests: Array<{ orgId?: string; sinceSeq: bigint }> = [];
  const client = {
    streamOrgUpdates: (req: { orgId?: string; sinceSeq: bigint }, { signal }: { signal: AbortSignal }) => {
      requests.push({ orgId: req.orgId, sinceSeq: req.sinceSeq });
      const script = scripts.shift() ?? { events: [], then: "stay" as const };
      return (async function* () {
        for (const e of script.events) yield e;
        if (script.then instanceof Error) throw script.then;
        if (script.then === "stay") {
          await new Promise<void>((_, reject) =>
            signal.addEventListener("abort", () => reject(new ConnectError("aborted", Code.Canceled))),
          );
        }
      })();
    },
  };
  return { client, requests };
}

const updates = (latest: bigint, ...ids: string[]) =>
  create(OrgStreamEventSchema, {
    event: {
      case: "updates",
      value: create(OrgUpdateBatchSchema, {
        latestSequence: latest,
        updates: ids.map((id) => create(OrgUpdateSchema, { resourceId: id, type: OrgUpdateType.CHANGED })),
      }),
    },
  });
const caughtUp = (latest: bigint) =>
  create(OrgStreamEventSchema, { event: { case: "caughtUp", value: { latestSequence: latest } as never } });

let stream: InstanceType<typeof OrgFeedStream> | undefined;
afterEach(() => {
  stream?.stop();
  vi.useRealTimers();
});

describe("OrgFeedStream", () => {
  it("forwards rows and resumes after the last sequence it processed", async () => {
    vi.useFakeTimers();
    const { client, requests } = fakeClient([
      { events: [caughtUp(5n), updates(7n, "c1")], then: "end" },
      { events: [], then: "stay" },
    ]);
    const onUpdates = vi.fn();
    const onCaughtUp = vi.fn();
    stream = new OrgFeedStream({ onUpdates, onCaughtUp, onRefused: vi.fn() }, () => client as never);
    stream.start("acme");
    await vi.advanceTimersByTimeAsync(0);
    expect(onCaughtUp).toHaveBeenCalledWith(5n);
    expect(onUpdates).toHaveBeenCalledWith([expect.objectContaining({ resourceId: "c1" })]);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(requests).toEqual([
      { orgId: "acme", sinceSeq: 0n },
      { orgId: "acme", sinceSeq: 7n },
    ]);
  });

  it("stops on a refusal instead of retrying it", async () => {
    vi.useFakeTimers();
    const refusal = new ConnectError("not a member", Code.PermissionDenied);
    const { client, requests } = fakeClient([{ events: [], then: refusal }]);
    const onRefused = vi.fn();
    stream = new OrgFeedStream({ onUpdates: vi.fn(), onCaughtUp: vi.fn(), onRefused }, () => client as never);
    stream.start("acme");
    await vi.advanceTimersByTimeAsync(60_000);
    expect(onRefused).toHaveBeenCalledWith(refusal);
    expect(requests).toHaveLength(1);
  });

  it("treats only a refusal as final", () => {
    expect(isOrgFeedRefusal(new ConnectError("x", Code.PermissionDenied))).toBe(true);
    expect(isOrgFeedRefusal(new ConnectError("x", Code.NotFound))).toBe(true);
    expect(isOrgFeedRefusal(new ConnectError("x", Code.Unauthenticated))).toBe(true);
    expect(isOrgFeedRefusal(new ConnectError("x", Code.Unavailable))).toBe(false);
    expect(isOrgFeedRefusal(new Error("x"))).toBe(false);
  });
});
