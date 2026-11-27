// The contract of fetchChangesSnapshot: one Connect unary POST with the
// token as a bearer, the snapshot handed back as delivered, and failures
// that reject without ever carrying the token. The twin of
// pkg/changes/controlplane_test.go.
import { describe, expect, it, vi } from "vitest";

import { fetchChangesSnapshot } from "./changes-server.js";

const TOKEN = "fct_secret-token-value";
const SNAPSHOT = '{"schema":"forge.changes/v1","revision":4,"changes":{}}';

function respond(body: string, status = 200) {
  return vi.fn(
    async (_url: string, _init?: RequestInit) => new Response(body, { status }),
  );
}

const cfg = {
  baseUrl: "https://cp.example/",
  environmentId: "env-1",
  token: TOKEN,
};

describe("fetchChangesSnapshot", () => {
  it("POSTs GetChangesSnapshot as JSON with the token as a bearer", async () => {
    const fetchImpl = respond(
      JSON.stringify({
        snapshotJson: SNAPSHOT,
        revision: "4",
        etag: "abc",
        problems: [{ key: "k", message: "variant gone" }],
      }),
    );
    const snap = await fetchChangesSnapshot({
      ...cfg,
      fetch: fetchImpl as unknown as typeof fetch,
    });
    expect(snap).toEqual({
      snapshotJson: SNAPSHOT,
      revision: 4,
      etag: "abc",
      problems: [{ key: "k", message: "variant gone" }],
    });
    const [url, init] = fetchImpl.mock.calls[0]!;
    expect(url).toBe(
      "https://cp.example/controlplane.v1.ChangesService/GetChangesSnapshot",
    );
    expect(init?.method).toBe("POST");
    expect(init?.headers).toEqual({
      "Content-Type": "application/json",
      "Connect-Protocol-Version": "1",
      Authorization: `Bearer ${TOKEN}`,
    });
    expect(init?.body).toBe('{"environmentId":"env-1"}');
  });

  it("rejects a refusal with the server's code, never the token", async () => {
    const fetchImpl = respond(
      '{"code":"permission_denied","message":"missing scope changes:read"}',
      403,
    );
    const err = await fetchChangesSnapshot({
      ...cfg,
      fetch: fetchImpl as unknown as typeof fetch,
    }).catch((e: unknown) => e as Error);
    expect(err).toBeInstanceOf(Error);
    expect((err as Error).message).toContain(
      "HTTP 403 permission_denied: missing scope changes:read",
    );
    expect((err as Error).message).not.toContain(TOKEN);
  });

  it("rejects a malformed response and an incomplete config", async () => {
    await expect(
      fetchChangesSnapshot({
        ...cfg,
        fetch: respond("not json") as unknown as typeof fetch,
      }),
    ).rejects.toThrow();
    await expect(
      fetchChangesSnapshot({ ...cfg, baseUrl: "cp.example" }),
    ).rejects.toThrow(/baseUrl/);
    await expect(fetchChangesSnapshot({ ...cfg, token: " " })).rejects.toThrow(
      /token/,
    );
  });

  it("aborts after the timeout", async () => {
    const hung = vi.fn(
      (_url: string, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    await expect(
      fetchChangesSnapshot({
        ...cfg,
        fetch: hung as unknown as typeof fetch,
        timeoutMs: 5,
      }),
    ).rejects.toThrow(/abort/);
  });
});
