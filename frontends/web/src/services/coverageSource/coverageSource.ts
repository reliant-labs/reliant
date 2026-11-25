// Copyright (c) 2026 Reliant Labs

/**
 * Where the coverage browser gets a file's source: the project's linked
 * GitHub repository, read at view time by the git blob id the coverage
 * report names (forge docs/proposals/tests-coverage-flakes.md, Amendment 1
 * part A). No source is ever stored, by reliant or the control plane: a
 * report carries paths, positions, hit counts and blob ids, never text.
 *
 * The seam is `fetchCoverageSource(project, blobId)`, behind
 * CoverageSourceContext: `{ text }`, or `{ unavailable: reason }` when there
 * is no source to show. The page then shows line numbers and hit counts with
 * one line saying why, and every coverage figure stays (they never needed
 * source).
 *
 * The real fetch is reliant's `CoverageSourceService.GetCoverageSource(project,
 * blob_id)`, which reads the blob from the project's GitHub repository
 * (`GET /repos/{o}/{r}/git/blobs/{sha}`) with the viewer's own GitHub token,
 * and serves only blob ids a retained report of that project names (it asks
 * control-plane first). A reliant without the RPC (Unimplemented) is
 * `not-yet-available`; previews and tests hand the context a fixture
 * (components/Forge/Tests/coverageFixtures.ts).
 *
 * Whatever answers, the page draws highlights only over text whose git blob
 * id IS the report's (`verifySource`): coverage shown over other bytes is
 * silently wrong, so a mismatch is one more reason, never a best effort.
 */

import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";

import { CoverageSourceService, CoverageSourceUnavailable } from "@/gen/reliant/v1/coverage_source_pb";
import { connectClient, transport as appTransport } from "@/lib/connect";

/**
 * Why a file has no source to show.
 *
 *   no-repo            the project has no linked GitHub repository (a local
 *                      folder, GitLab, Bitbucket, an unreachable GitHub
 *                      Enterprise Server)
 *   uncommitted        the run had uncommitted changes: the blob is in no commit
 *   gone               GitHub no longer has the blob (history rewritten, the
 *                      repository deleted or moved)
 *   revoked            the viewer's GitHub is not connected, needs reconnecting,
 *                      or GitHub refused their token
 *   not-yet-available  this deployment cannot fetch source yet (no
 *                      GetCoverageSource, or its control plane cannot say which
 *                      blobs a report holds)
 *   too-large          the file is larger than the viewer shows (2 MiB)
 *   mismatch           what came back is not the blob the report names
 *   unreachable        GitHub or reliant did not answer
 */
export type CoverageSourceUnavailableReason =
  | "no-repo"
  | "uncommitted"
  | "gone"
  | "revoked"
  | "not-yet-available"
  | "too-large"
  | "mismatch"
  | "unreachable";

export type CoverageSource = { text: string } | { unavailable: CoverageSourceUnavailableReason };

export interface CoverageSourceOptions {
  /** The org the read acts in; unset: the caller's default org. */
  orgId?: string | null;
  signal?: AbortSignal;
}

export type FetchCoverageSource = (project: string, blobId: string, opts?: CoverageSourceOptions) => Promise<CoverageSource>;

const REASONS: Partial<Record<CoverageSourceUnavailable, CoverageSourceUnavailableReason>> = {
  [CoverageSourceUnavailable.NO_REPO]: "no-repo",
  [CoverageSourceUnavailable.UNCOMMITTED]: "uncommitted",
  [CoverageSourceUnavailable.GONE]: "gone",
  [CoverageSourceUnavailable.REVOKED]: "revoked",
  [CoverageSourceUnavailable.NOT_YET_AVAILABLE]: "not-yet-available",
  [CoverageSourceUnavailable.TOO_LARGE]: "too-large",
};

/**
 * The real fetch: reliant's GetCoverageSource. A file the run changed without
 * committing has no blob, and nothing is asked. Every reason is an answer; a
 * reliant without the RPC is `not-yet-available`; any other failure throws
 * (the page shows `unreachable`). The bytes are decoded as UTF-8 here and
 * re-hashed by `verifySource` before anything is drawn over them.
 *
 * `transport` is the caller's TransportScope's (lib/transportScope.tsx): a
 * scope's own transport (a fixture pack's) gets a client on it; none, or the
 * app's, is the app's client.
 */
export async function fetchCoverageSource(project: string, blobId: string, opts?: CoverageSourceOptions, transport?: Transport): Promise<CoverageSource> {
  if (!blobId) return { unavailable: "uncommitted" };
  try {
    const client = transport && transport !== appTransport ? createClient(CoverageSourceService, transport) : connectClient(CoverageSourceService);
    const res = await client.getCoverageSource(
      { project, blobId, orgId: opts?.orgId ?? undefined },
      { signal: opts?.signal }
    );
    switch (res.result.case) {
      case "content":
        return { text: new TextDecoder().decode(res.result.value) };
      case "unavailable":
        // A reason this build does not know yet: no source, and no guess why.
        return { unavailable: REASONS[res.result.value] ?? "unreachable" };
      default:
        return { unavailable: "unreachable" };
    }
  } catch (err) {
    if (ConnectError.from(err).code === Code.Unimplemented) return { unavailable: "not-yet-available" };
    throw err;
  }
}

/** The reason, or null when there is source. */
export function unavailableReason(source: CoverageSource): CoverageSourceUnavailableReason | null {
  return "unavailable" in source ? source.unavailable : null;
}

// --- Verification: is this text the blob the report names? ---

/** SHA-1 of `bytes`, as lowercase hex. Pure: no secure context needed (crypto.subtle is absent over plain http). */
export function sha1Hex(bytes: Uint8Array): string {
  const length = bytes.length;
  const words = new Uint32Array((((length + 8) >>> 6) + 1) * 16);
  for (let i = 0; i < length; i++) words[i >>> 2]! |= bytes[i]! << (24 - (i % 4) * 8);
  words[length >>> 2]! |= 0x80 << (24 - (length % 4) * 8);
  const bits = length * 8;
  words[words.length - 2] = Math.floor(bits / 0x100000000);
  words[words.length - 1] = bits >>> 0;

  let h0 = 0x67452301;
  let h1 = 0xefcdab89;
  let h2 = 0x98badcfe;
  let h3 = 0x10325476;
  let h4 = 0xc3d2e1f0;
  const w = new Uint32Array(80);
  for (let block = 0; block < words.length; block += 16) {
    for (let t = 0; t < 16; t++) w[t] = words[block + t]!;
    for (let t = 16; t < 80; t++) {
      const x = w[t - 3]! ^ w[t - 8]! ^ w[t - 14]! ^ w[t - 16]!;
      w[t] = (x << 1) | (x >>> 31);
    }
    let a = h0;
    let b = h1;
    let c = h2;
    let d = h3;
    let e = h4;
    for (let t = 0; t < 80; t++) {
      let f: number;
      let k: number;
      if (t < 20) {
        f = (b & c) | (~b & d);
        k = 0x5a827999;
      } else if (t < 40) {
        f = b ^ c ^ d;
        k = 0x6ed9eba1;
      } else if (t < 60) {
        f = (b & c) | (b & d) | (c & d);
        k = 0x8f1bbcdc;
      } else {
        f = b ^ c ^ d;
        k = 0xca62c1d6;
      }
      const temp = (((a << 5) | (a >>> 27)) + f + e + k + w[t]!) >>> 0;
      e = d;
      d = c;
      c = (b << 30) | (b >>> 2);
      b = a;
      a = temp;
    }
    h0 = (h0 + a) >>> 0;
    h1 = (h1 + b) >>> 0;
    h2 = (h2 + c) >>> 0;
    h3 = (h3 + d) >>> 0;
    h4 = (h4 + e) >>> 0;
  }
  return [h0, h1, h2, h3, h4].map((h) => (h >>> 0).toString(16).padStart(8, "0")).join("");
}

/** Git's blob id of `text` (UTF-8): sha1("blob <byte length>\0" + content), what `git hash-object` prints. */
export function gitBlobId(text: string): string {
  const content = new TextEncoder().encode(text);
  const header = new TextEncoder().encode(`blob ${content.length}\0`);
  const bytes = new Uint8Array(header.length + content.length);
  bytes.set(header);
  bytes.set(content, header.length);
  return sha1Hex(bytes);
}

/**
 * The source, only if it is exactly the blob the report names and reaches
 * every line the report covers; otherwise `mismatch`. A file whose bytes are
 * not valid UTF-8 (or that carries a byte-order mark) does not survive the
 * round trip through text, and fails closed here too.
 */
export function verifySource(source: CoverageSource, blobId: string, lastCoveredLine: number): CoverageSource {
  if ("unavailable" in source) return source;
  if (!blobId || gitBlobId(source.text).toLowerCase() !== blobId.toLowerCase()) return { unavailable: "mismatch" };
  if (lastCoveredLine > sourceLineCount(source.text)) return { unavailable: "mismatch" };
  return source;
}

/** Lines in `text` as a viewer numbers them: a final newline ends the last line, it does not start another. */
export function sourceLineCount(text: string): number {
  if (text === "") return 0;
  const lines = text.split("\n").length;
  return text.endsWith("\n") ? lines - 1 : lines;
}
