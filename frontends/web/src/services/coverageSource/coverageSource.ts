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
 * NOT WIRED YET. The real fetch is reliant's `GetCoverageSource(project,
 * blob_id)` RPC (task W2b), which reads the blob through the project's
 * GitHub connection (`GET /repos/{o}/{r}/git/blobs/{sha}`) and serves only
 * blob ids a retained report of that project names. Until it exists, the
 * real implementation answers `not-yet-available`; previews and tests hand
 * the context a fixture (components/Forge/Tests/coverageFixtures.ts).
 *
 * Whatever answers, the page draws highlights only over text whose git blob
 * id IS the report's (`verifySource`): coverage shown over other bytes is
 * silently wrong, so a mismatch is one more reason, never a best effort.
 */

/**
 * Why a file has no source to show.
 *
 *   no-repo            the project has no linked GitHub repository (a local
 *                      folder, GitLab, Bitbucket, an unreachable GitHub
 *                      Enterprise Server)
 *   uncommitted        the run had uncommitted changes: the blob is in no commit
 *   gone               GitHub no longer has the blob (history rewritten, the
 *                      repository deleted or moved)
 *   revoked            the GitHub connection that reads it was revoked, or no
 *                      longer reaches the repository
 *   not-yet-available  this build cannot fetch source yet (GetCoverageSource, W2b)
 *   mismatch           what came back is not the blob the report names
 *   unreachable        GitHub or reliant did not answer
 */
export type CoverageSourceUnavailableReason = "no-repo" | "uncommitted" | "gone" | "revoked" | "not-yet-available" | "mismatch" | "unreachable";

export type CoverageSource = { text: string } | { unavailable: CoverageSourceUnavailableReason };

export interface CoverageSourceOptions {
  /** The org the read acts in; unset: the caller's default org. */
  orgId?: string | null;
  signal?: AbortSignal;
}

export type FetchCoverageSource = (project: string, blobId: string, opts?: CoverageSourceOptions) => Promise<CoverageSource>;

/**
 * The real fetch. Until reliant's GetCoverageSource exists (W2b), there is no
 * source to read: every file shows its line numbers and hits, and says so.
 */
export const fetchCoverageSource: FetchCoverageSource = (_project, blobId) =>
  Promise.resolve(blobId ? { unavailable: "not-yet-available" } : { unavailable: "uncommitted" });

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
