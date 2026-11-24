// Copyright (c) 2025 Reliant Labs

/**
 * The dev previews' data seam (../preview.tsx FixturePreview) and the overlay
 * helpers it is used with (../overlay.ts): the subtree's real clients read the
 * pack through lib/connect.ts's module transport, an overlay wins over the
 * pack per method, the transport it replaced comes back on unmount, and a
 * second preview mounted beside the first is refused.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { render, screen, waitFor } from "@testing-library/react";
import { useEffect, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ListBrowsableBucketsResponseSchema, StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { connectClient, setTransport, transport } from "@/lib/connect";

import { answering, emptying, failing, latency, stalling } from "../overlay";
import { FixturePreview, withOverlay } from "../preview";
import { createFixtureTransport, defineFixturePack } from "../transport";

const BUCKETS = `${StorageBrowserService.typeName}/ListBrowsableBuckets`;

const PACK = defineFixturePack({
  name: "test",
  scenario: { [BUCKETS]: () => create(ListBrowsableBucketsResponseSchema, { buckets: [{ name: "media" }] }) },
});
const SERVE = { readOnly: true };

/** Reads the bucket names through the app's own client, as a page would. */
function Buckets() {
  const [text, setText] = useState("…");
  useEffect(() => {
    connectClient(StorageBrowserService)
      .listBrowsableBuckets({})
      .then((res) => setText(res.buckets.map((bucket) => bucket.name).join(",") || "none"))
      .catch((err) => setText(`error:${Code[ConnectError.from(err).code]}`));
  }, []);
  return <p data-testid="buckets">{text}</p>;
}

afterEach(() => setTransport(null));

describe("FixturePreview", () => {
  it("serves the subtree's real clients from the pack, and restores the transport it replaced", async () => {
    const outer = { unary: vi.fn(), stream: vi.fn() } as unknown as Transport;
    setTransport(outer);
    const view = render(
      <FixturePreview pack={PACK} serve={SERVE}>
        <Buckets />
      </FixturePreview>
    );
    await waitFor(() => expect(screen.getByTestId("buckets").textContent).toBe("media"));
    view.unmount();
    // The module transport routes to the one it replaced again.
    await createClient(StorageBrowserService, transport).listBrowsableBuckets({}).catch(() => {});
    expect(outer.unary).toHaveBeenCalled();
  });

  it("puts an overlay over the pack, per method", async () => {
    render(
      <FixturePreview pack={PACK} serve={SERVE} overlay={failing("down", StorageBrowserService, Code.Unavailable)}>
        <Buckets />
      </FixturePreview>
    );
    await waitFor(() => expect(screen.getByTestId("buckets").textContent).toBe("error:Unavailable"));
  });

  it("refuses a second preview while one owns the transport", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(() =>
      render(
        <>
          <FixturePreview pack={PACK} serve={SERVE}>
            <Buckets />
          </FixturePreview>
          <FixturePreview pack={PACK} serve={SERVE}>
            <Buckets />
          </FixturePreview>
        </>
      )
    ).toThrow(/another fixture preview is mounted/);
    spy.mockRestore();
  });
});

describe("overlay helpers", () => {
  const client = (overlay: Parameters<typeof withOverlay>[1], interceptors = [] as ReturnType<typeof latency>[]) =>
    createClient(StorageBrowserService, createFixtureTransport({ ...SERVE, interceptors, pack: withOverlay(PACK, overlay) }));

  it("emptying answers the empty message, answering whatever it returns", async () => {
    expect((await client(emptying("none", StorageBrowserService)).listBrowsableBuckets({})).buckets).toEqual([]);
    const one = answering("one", StorageBrowserService.method.listBrowsableBuckets, () => create(ListBrowsableBucketsResponseSchema, { buckets: [{ name: "logs" }] }));
    expect((await client(one).listBrowsableBuckets({})).buckets.map((b) => b.name)).toEqual(["logs"]);
  });

  it("answering reaches a write the read-only guard would refuse", async () => {
    const presign = StorageBrowserService.method.presignObjectDownload;
    expect(await client(null).presignObjectDownload({}).then(() => "ok", (err) => Code[ConnectError.from(err).code])).toBe("PermissionDenied");
    const refused = failing("refused", presign, Code.FailedPrecondition, "bucket gone");
    expect(await client(refused).presignObjectDownload({}).then(() => "ok", (err) => ConnectError.from(err).rawMessage)).toBe("bucket gone");
  });

  it("stalling stays in flight until aborted; latency holds the answer", async () => {
    const controller = new AbortController();
    const call = client(stalling("loading", StorageBrowserService)).listBrowsableBuckets({}, { signal: controller.signal });
    const settled = vi.fn();
    void call.then(settled, settled);
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(settled).not.toHaveBeenCalled();
    controller.abort();
    await expect(call).rejects.toThrow();

    const started = Date.now();
    await client(null, [latency(40)]).listBrowsableBuckets({});
    expect(Date.now() - started).toBeGreaterThanOrEqual(35);
  });
});
