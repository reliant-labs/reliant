// Copyright (c) 2026 Reliant Labs

/**
 * shop's Security pages (Vulnerabilities, SBOMs, the Overview's card, a
 * release's findings): every release's artifacts and their SBOMs, and what
 * the scanner found in them, told in shopStory's terms. Security/vulnDataset.ts
 * answers it the way control-plane's VulnerabilityService would, and
 * handlers/security.ts puts those answers on the wire.
 *
 *   artifacts  each release's api, worker and inventory images (SBOMs syft
 *              made from the image) and the shop-web static site (its SBOM is
 *              its lockfile). An artifact a release did not change keeps the
 *              digest, and so the SBOM, of the release that last did
 *              (artifactVersion), so its findings are the same ones.
 *   findings   golang.org/x/net in every backend: a fixable high disclosed
 *              three days ago, so it reached every environment before
 *              anyone knew; path-to-regexp in shop-web's lockfile, a fixable
 *              npm high; busybox in the backend images' base, a medium with
 *              no fix; golang.org/x/crypto in the api, a high that prod and
 *              staging exempt (the api runs no SSH server) until it expires.
 *              And one that is gone: connect-go v1.18.1's high, which jon's
 *              "chore(deps): bump connect-go to v1.19" (v1.8.3) resolved, in
 *              each environment as v1.8.3 reached it.
 *   policy     prod's vuln_gate enforces at critical, so nothing it runs
 *              blocks (and v1.8.3's promotion passed it); staging audits the
 *              stricter bar it is trying out, high, so the two fixable highs
 *              show as what it would block; dev declares no gate.
 *   scans      every SBOM was rescanned five hours ago against a database
 *              built seven hours ago; v1.8.4's new api SBOM was scanned when
 *              it was cut. Nothing is pending, failed or stale.
 *
 * Releases, versions, commits, environments and who runs what are
 * shopStory's (DEMO_RELEASES, RUNNING, demoLedger), never restated here.
 */

import type { ArtifactSbom, Advisory, FixState, VulnDatabase, VulnPackage } from "@/services/controlPlane/vulnerabilities/types";
import { vulnDatasetEngine, type DatasetEnv, type DatasetFinding, type DatasetRelease, type VulnDataset, type VulnDatasetEngine } from "@/components/Forge/Security/vulnDataset";

import { artifactDigest } from "../handlers/console";
import {
  artifactVersion,
  DAY,
  DEMO_BACKENDS,
  DEMO_FRONTEND,
  DEMO_RELEASES,
  demoLedger,
  demoNow,
  demoRelease,
  HOUR,
  MINUTE,
  RUNNING,
  type DemoEnvKey,
} from "../shopStory";

/** The shop's artifacts, in the order a release lists them: three images and the static storefront. */
export const SHOP_ARTIFACTS = [...DEMO_BACKENDS, DEMO_FRONTEND] as const;

/** Where the backend images are pushed (handlers/console.ts's DeployArtifact uri). */
const REGISTRY = "registry.example.com/shop";

/** prod's and staging's vuln_gate, and dev's lack of one (the envs' KCL). */
export const SHOP_VULN_POLICY: Record<DemoEnvKey, DatasetEnv["policy"]> = {
  "env-prod": { mode: "enforce", blockAt: "critical" },
  "env-staging": { mode: "audit", blockAt: "high" },
  "env-dev": null,
};

/** When the nightly rescan ran, and when the database it used was built. */
const RESCAN_AGO = 5 * HOUR;
const DATABASE_AGO = 7 * HOUR;
/** How long after a release's cut its new SBOMs were first scanned. */
const SCAN_AFTER_CUT = 3 * MINUTE;

const pkg = (name: string, version: string, type: string, purl: string): VulnPackage => ({ name, version, type, purl, location: "" });
const advisory = (a: Omit<Advisory, "references" | "kev"> & { kev?: boolean }): Advisory => ({ kev: false, ...a, references: [] });

/**
 * One advisory as it lands in shop: which artifacts' SBOMs hold which version
 * of the package, by the release that last built the artifact (null: that
 * build holds no affected version).
 */
interface ShopAdvisory {
  advisory: Advisory;
  fixedVersions: string[];
  fixState: FixState;
  /** Before now: when the scanner's database first matched it. */
  knownAgo: number;
  /** The affected package in `artifact` as built by release `built`, or null. */
  package: (artifact: string, built: string) => VulnPackage | null;
  /** Prod's and staging's exemption, if any. */
  exempt?: { reason: string; expiresIn: number };
}

const older = (version: string, than: string) => DEMO_RELEASES.findIndex((r) => r.version === version) > DEMO_RELEASES.findIndex((r) => r.version === than);
const backend = (artifact: string) => (DEMO_BACKENDS as readonly string[]).includes(artifact);

const goModule = (path: string, version: string) => pkg(path, version, "go-module", `pkg:golang/${path}@${version}`);

export const SHOP_ADVISORIES: ShopAdvisory[] = [
  {
    advisory: advisory({
      id: "GHSA-4v7x-pqm2-hc8w",
      aliases: ["CVE-2026-31042"],
      severity: "high",
      cvss: 7.5,
      epss: 0.018,
      summary: "HTTP/2 server: a client's stream of CONTINUATION frames keeps the server decoding headers without bound.",
    }),
    fixedVersions: ["v0.38.0"],
    fixState: "fixed",
    knownAgo: 3 * DAY + RESCAN_AGO,
    package: (artifact) => (backend(artifact) ? goModule("golang.org/x/net", "v0.35.0") : null),
  },
  {
    advisory: advisory({
      id: "GHSA-9wq3-vr5f-mx2j",
      aliases: ["CVE-2026-28817"],
      severity: "high",
      cvss: 7.5,
      epss: 0.009,
      summary: "A route with two parameters in one segment compiles to a backtracking regular expression (ReDoS).",
    }),
    fixedVersions: ["6.3.0"],
    fixState: "fixed",
    knownAgo: 2 * DAY + RESCAN_AGO,
    package: (artifact) => (artifact === DEMO_FRONTEND ? pkg("path-to-regexp", "6.2.1", "npm", "pkg:npm/path-to-regexp@6.2.1") : null),
  },
  {
    advisory: advisory({
      id: "CVE-2026-27104",
      aliases: [],
      severity: "medium",
      cvss: 5.5,
      epss: 0.002,
      summary: "busybox tar: a crafted member name can write outside the extraction directory.",
    }),
    fixedVersions: [],
    fixState: "not-fixed",
    knownAgo: 30 * DAY,
    package: (artifact) => (backend(artifact) ? pkg("busybox", "1.37.0-r12", "apk", "pkg:apk/alpine/busybox@1.37.0-r12") : null),
  },
  {
    advisory: advisory({
      id: "GHSA-hp3c-8m9w-r7xq",
      aliases: ["CVE-2026-24417"],
      severity: "high",
      cvss: 7.4,
      epss: 0.006,
      summary: "ssh: a server's PublicKeyCallback can accept a key the client never proved it holds.",
    }),
    fixedVersions: ["v0.36.0"],
    fixState: "fixed",
    knownAgo: 11 * DAY,
    package: (artifact) => (artifact === "api" ? goModule("golang.org/x/crypto", "v0.33.0") : null),
    exempt: {
      reason: "The api runs no SSH server: nothing it builds imports x/crypto/ssh. Bump with the next dependency sweep (acme/shop#87).",
      expiresIn: 18 * DAY,
    },
  },
  {
    advisory: advisory({
      id: "GHSA-c7m2-5xvf-q4jh",
      aliases: ["CVE-2026-20391"],
      severity: "high",
      cvss: 7.5,
      epss: 0.011,
      summary: "A gzip-compressed request can expand past the server's read limit before the limit applies.",
    }),
    fixedVersions: ["v1.19.0"],
    fixState: "fixed",
    knownAgo: 12 * DAY,
    // v1.8.3's "chore(deps): bump connect-go to v1.19" rebuilt every backend on the fixed version.
    package: (artifact, built) => (backend(artifact) && older(built, "v1.8.3") ? goModule("connectrpc.com/connect", "v1.18.1") : null),
  },
];

/** A clearly synthetic, stable `sha256:` digest of `seed`. */
function digestOf(seed: string): string {
  let out = "";
  for (let i = 0; out.length < 64; i++) {
    let h = 0x811c9dc5 ^ i;
    for (const c of `${seed}#${i}`) h = Math.imul(h ^ c.charCodeAt(0), 0x01000193) >>> 0;
    out += h.toString(16).padStart(8, "0");
  }
  return `sha256:${out.slice(0, 64)}`;
}

/** SBOM components per artifact, as each release built it. */
const COMPONENTS: Record<string, number> = { api: 214, worker: 168, inventory: 152, [DEMO_FRONTEND]: 1_136 };

/** One release's artifact and its SBOM: the SBOM of the build it reuses, scanned at that build's cut and by every rescan since. */
function artifactOf(name: string, version: string, now: number): ArtifactSbom {
  const built = artifactVersion(name, version);
  const cutAgo = demoRelease(built)!.age;
  const site = name === DEMO_FRONTEND;
  return {
    artifactName: name,
    sbomDigest: digestOf(`sbom:${name}@${built}`),
    format: "cyclonedx-json@1.6",
    generator: "syft@v1.57.2",
    source: site ? "lockfile" : "image",
    referrer: site ? "" : `${REGISTRY}/${name}@${artifactDigest(name, version)}`,
    componentCount: COMPONENTS[name]! + DEMO_RELEASES.length - 1 - DEMO_RELEASES.findIndex((r) => r.version === built),
    scanState: "scanned",
    lastScannedMs: now - Math.min(RESCAN_AGO, cutAgo - SCAN_AFTER_CUT),
    scanError: "",
  };
}

function findingsOf(version: string, now: number): DatasetFinding[] {
  const out: DatasetFinding[] = [];
  for (const a of SHOP_ADVISORIES) {
    // One finding per package version: the artifacts holding that same version share it.
    const byPurl = new Map<string, DatasetFinding>();
    for (const artifact of SHOP_ARTIFACTS) {
      const found = a.package(artifact, artifactVersion(artifact, version));
      if (!found) continue;
      const prev = byPurl.get(found.purl);
      if (prev) prev.artifactNames.push(artifact);
      else byPurl.set(found.purl, { advisory: a.advisory, package: found, fixedVersions: a.fixedVersions, fixState: a.fixState, artifactNames: [artifact], knownSinceMs: now - a.knownAgo });
    }
    out.push(...byPurl.values());
  }
  return out;
}

const ENV_ORDER: DemoEnvKey[] = ["env-prod", "env-staging", "env-dev"];
const ENV_NAME: Record<DemoEnvKey, string> = { "env-prod": "prod", "env-staging": "staging", "env-dev": "dev" };

/** shop's vulnerability dataset as of `now` (the page load). */
export function shopVulnDataset(now: number = demoNow()): VulnDataset {
  const ledger = demoLedger(now);
  const releases: DatasetRelease[] = DEMO_RELEASES.map((r) => ({
    releaseId: `rel-${r.version}`,
    version: r.version,
    artifacts: SHOP_ARTIFACTS.map((name) => artifactOf(name, r.version, now)),
    findings: findingsOf(r.version, now),
  }));
  const envs: DatasetEnv[] = ENV_ORDER.map((id) => ({
    id,
    name: ENV_NAME[id],
    releaseVersion: RUNNING[id].version,
    policy: SHOP_VULN_POLICY[id],
    exemptions:
      id === "env-dev"
        ? []
        : SHOP_ADVISORIES.filter((a) => a.exempt).map((a) => ({ environmentId: id, advisoryId: a.advisory.id, reason: a.exempt!.reason, expiresMs: now + a.exempt!.expiresIn })),
    promotions: ledger.filter((p) => p.envId === id).map((p) => ({ promotionId: p.promotionId, releaseVersion: p.version, targets: p.targets, atMs: p.atMs })),
  }));
  const database: VulnDatabase = { scanner: "grype", scannerVersion: "v0.122.1", schema: "v6.1.10", builtAtMs: now - DATABASE_AGO, stale: false };
  return { database, envs, releases };
}

/** VulnerabilityService's answers over shop's dataset, measured from the page load (handlers/security.ts serves them). */
export function shopVulnEngine(now: number = demoNow()): VulnDatasetEngine {
  return vulnDatasetEngine(shopVulnDataset(now));
}
