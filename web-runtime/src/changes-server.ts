// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Forge Changes for an app NOT on Reliant hosting, on the SERVER side: fetch
// the environment's snapshot from the control plane with an access token
// carrying `changes:read`. The twin of forge/pkg/changes's OpenControlPlane.
//
// ── Server only ──────────────────────────────────────────────────────────
//
// The token must never reach a browser. Call this in Node, a server-rendered
// route or an edge function, and serve the result to the page yourself at
// `/_forge/changes.json`, where loadChanges() looks for it: the snapshot a
// browser reads is public, the token that fetches it is not.
//
//   import { fetchChangesSnapshot } from "@reliantlabs/forge-web-runtime/changes-server";
//
//   // e.g. an Express route; any server framework works the same way
//   app.get("/_forge/changes.json", async (_req, res) => {
//     const snap = await fetchChangesSnapshot({
//       baseUrl: process.env.FORGE_CHANGES_URL!,
//       environmentId: process.env.FORGE_CHANGES_ENVIRONMENT_ID!,
//       token: process.env.FORGE_CHANGES_TOKEN!,
//     });
//     res.set("Cache-Control", "no-cache").type("application/json").send(snap.snapshotJson);
//   });
//
// Server rendering evaluates from the same fetch through loadChanges's
// `source` option:
//
//   const changes = await loadChanges({
//     source: async () => (await fetchChangesSnapshot(cfg)).snapshotJson,
//   });
//
// Cache the result for a few seconds (the Go store polls every 15s) rather
// than fetching per request.
//
// Imports nothing: a fetch, and only when called. It lives on its own
// subpath, outside the barrel, so a browser bundle never carries it.

/** The Connect procedure that serves an environment's snapshot. */
const SNAPSHOT_PROCEDURE = "/controlplane.v1.ChangesService/GetChangesSnapshot";

/** fetchChangesSnapshot's default timeout, as the Go store's. */
export const DEFAULT_CHANGES_FETCH_TIMEOUT_MS = 10_000;

/** Where fetchChangesSnapshot fetches from. */
export interface FetchChangesSnapshotOptions {
  /** The control plane's base URL (`FORGE_CHANGES_URL`). */
  baseUrl: string;
  /** The environment whose snapshot to fetch (`FORGE_CHANGES_ENVIRONMENT_ID`). */
  environmentId: string;
  /**
   * An access token carrying `changes:read` (`FORGE_CHANGES_TOKEN`). Sent as
   * a bearer token; never put it in anything a browser loads.
   */
  token: string;
  /** The fetch to use (tests, a custom agent). Default the global one. */
  fetch?: typeof fetch;
  /** Abort after this long. Default 10s. */
  timeoutMs?: number;
}

/** A problem the control plane found rendering the snapshot. */
export interface ChangesSnapshotProblem {
  /** The change key. */
  key: string;
  /** Why the change was delivered off or left out. */
  message: string;
}

/** The environment's snapshot as the control plane delivers it. */
export interface ChangesSnapshotResponse {
  /**
   * The "forge.changes/v1" document, exactly as delivered: what to serve at
   * `/_forge/changes.json`. "" when the environment has none.
   */
  snapshotJson: string;
  /** The snapshot's revision. */
  revision: number;
  /** A strong validator of snapshotJson (hex SHA-256). */
  etag: string;
  /** Changes delivered off, or left out, and why. */
  problems: ChangesSnapshotProblem[];
}

interface WireResponse {
  snapshotJson?: unknown;
  revision?: unknown;
  etag?: unknown;
  problems?: unknown;
}

/**
 * Fetch the environment's snapshot from the control plane
 * (ChangesService.GetChangesSnapshot, a Connect unary call over JSON).
 * Rejects on a refused token, an unreachable control plane, a timeout or a
 * malformed response; the error never carries the token. Server only: see
 * the module header.
 */
export async function fetchChangesSnapshot(
  options: FetchChangesSnapshotOptions,
): Promise<ChangesSnapshotResponse> {
  const base = options.baseUrl.trim().replace(/\/+$/, "");
  if (!/^https?:\/\/[^/]/.test(base)) {
    throw new Error(
      "fetchChangesSnapshot: baseUrl must be an http(s) URL (FORGE_CHANGES_URL)",
    );
  }
  if (options.environmentId.trim() === "" || options.token.trim() === "") {
    throw new Error(
      "fetchChangesSnapshot: environmentId and token are required (FORGE_CHANGES_ENVIRONMENT_ID, FORGE_CHANGES_TOKEN)",
    );
  }
  const fetchImpl = options.fetch ?? globalThis.fetch;
  if (typeof fetchImpl !== "function") {
    throw new Error("fetchChangesSnapshot: no fetch is available");
  }
  const controller = new AbortController();
  const timer = setTimeout(
    () => controller.abort(),
    options.timeoutMs ?? DEFAULT_CHANGES_FETCH_TIMEOUT_MS,
  );
  try {
    const res = await fetchImpl(base + SNAPSHOT_PROCEDURE, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Connect-Protocol-Version": "1",
        Authorization: `Bearer ${options.token.trim()}`,
      },
      body: JSON.stringify({ environmentId: options.environmentId.trim() }),
      signal: controller.signal,
    });
    const text = await res.text();
    if (!res.ok) {
      throw new Error(
        `fetchChangesSnapshot: the control plane answered ${statusLine(res.status, text)}`,
      );
    }
    return decode(text);
  } finally {
    clearTimeout(timer);
  }
}

/** A non-OK status and, for a Connect error body, its code and message. */
function statusLine(status: number, body: string): string {
  try {
    const e = JSON.parse(body) as { code?: unknown; message?: unknown };
    if (typeof e.code === "string" && e.code !== "") {
      const msg =
        typeof e.message === "string"
          ? (e.message.split("\n")[0] ?? "").slice(0, 200)
          : "";
      return `HTTP ${status} ${e.code}: ${msg}`;
    }
  } catch {
    // not a Connect error body
  }
  return `HTTP ${status}`;
}

function decode(text: string): ChangesSnapshotResponse {
  const w = JSON.parse(text) as WireResponse;
  if (w === null || typeof w !== "object") {
    throw new Error("fetchChangesSnapshot: the response is not an object");
  }
  // protojson writes an int64 as a string.
  const revision =
    typeof w.revision === "string" || typeof w.revision === "number"
      ? Number(w.revision)
      : 0;
  const problems = Array.isArray(w.problems)
    ? w.problems.flatMap((p: unknown) => {
        if (p === null || typeof p !== "object") return [];
        const { key, message } = p as { key?: unknown; message?: unknown };
        return [
          {
            key: typeof key === "string" ? key : "",
            message: typeof message === "string" ? message : "",
          },
        ];
      })
    : [];
  return {
    snapshotJson: typeof w.snapshotJson === "string" ? w.snapshotJson : "",
    revision: Number.isFinite(revision) ? revision : 0,
    etag: typeof w.etag === "string" ? w.etag : "",
    problems,
  };
}
