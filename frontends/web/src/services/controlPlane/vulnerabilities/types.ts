// Copyright (c) 2025 Reliant Labs

/**
 * The plain types the Security pages (components/Forge/Security) speak: the
 * vulnerability findings of a project's releases and environments, as
 * control-plane's `controlplane.v1.VulnerabilityService` reports them
 * (control-plane proto/services/vulnerability/v1/vulnerability.proto, whose
 * header is the behaviour spec). Times are epoch ms (null when the server sent
 * none), enums are names, and nothing here knows the wire: wire.ts maps it.
 *
 * UNKNOWN IS NEVER CLEAN. An artifact with no SBOM, an SBOM whose scan is
 * pending or failed, and a scan against a stale database are each a scan
 * state of their own (ScanState), never zero findings.
 */

/** A finding's severity, as the scanner rates it. Ordered: SEVERITY_ORDER. */
export type Severity = "critical" | "high" | "medium" | "low" | "negligible" | "unknown";

/** Most severe first: the order every list and count here uses. */
export const SEVERITY_ORDER: readonly Severity[] = ["critical", "high", "medium", "low", "negligible", "unknown"];

/** The severities a "minimum severity" filter offers. UNKNOWN always passes one (a severity nobody rated is not a low one). */
export const MIN_SEVERITIES = ["critical", "high", "medium", "low"] as const;
export type MinSeverity = (typeof MIN_SEVERITIES)[number];

/** Whether a fixed version of the package exists. A finding is fixable exactly when this is "fixed". */
export type FixState = "fixed" | "not-fixed" | "wont-fix" | "unknown";

/**
 * How far the findings of an SBOM, a release or an environment can be trusted.
 *
 *   none     the environment has no release (nothing was ever promoted there)
 *   no-sbom  an artifact has no SBOM recorded: not scanned, never clean
 *   pending  the SBOM is stored and its first scan has not finished
 *   scanned  the newest scan succeeded against a current database
 *   failed   the newest scan failed; the previous scan's findings, if any, still count
 *   stale    the newest successful scan used a database past its freshness bound
 */
export type ScanState = "none" | "no-sbom" | "pending" | "scanned" | "failed" | "stale";

/** The scanner and database an answer was computed with. */
export interface VulnDatabase {
  scanner: string;
  scannerVersion: string;
  schema: string;
  /** When the database was built upstream. Null when unknown. */
  builtAtMs: number | null;
  /** Past the freshness bound: a verdict computed with it is "could not determine", not "clean". */
  stale: boolean;
}

export interface SeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
  negligible: number;
  unknown: number;
}

/** How much of a release the findings describe. */
export interface VulnCoverage {
  artifacts: number;
  withSbom: number;
  scanned: number;
}

/** One environment's current release, in counts. Exempt findings count in `exempt` only. */
export interface EnvVulnSummary {
  environmentId: string;
  environmentName: string;
  /** The release of the environment's newest promotion; empty when nothing was promoted there. */
  releaseId: string;
  releaseVersion: string;
  counts: SeverityCounts;
  kev: number;
  fixable: number;
  blocking: number;
  exempt: number;
  coverage: VulnCoverage;
  lastScannedMs: number | null;
  /** The least trustworthy state among the release's artifacts. */
  scanState: ScanState;
  /** When scanState is "failed": when scans began failing. */
  failingSinceMs: number | null;
}

export interface VulnSummary {
  environments: EnvVulnSummary[];
  database: VulnDatabase | null;
}

export interface Advisory {
  /** What the scanner matched on: "CVE-2026-1234", "GHSA-xxxx-xxxx-xxxx". */
  id: string;
  /** The same advisory under other ids. */
  aliases: string[];
  severity: Severity;
  cvss: number | null;
  /** On CISA's Known Exploited Vulnerabilities list. */
  kev: boolean;
  /** EPSS: probability of exploitation in the next 30 days, 0..1. */
  epss: number | null;
  /** One line; may be empty in a list. */
  summary: string;
  /** Links; only GetVulnerability fills them. */
  references: string[];
}

export interface VulnPackage {
  name: string;
  version: string;
  /** "go-module", "npm", "deb", … */
  type: string;
  purl: string;
  location: string;
}

/** An unexpired exemption declared in an environment's KCL. */
export interface VulnExemption {
  environmentId: string;
  advisoryId: string;
  reason: string;
  expiresMs: number | null;
}

/** One advisory in one package, across the artifacts and environments in the request's scope. */
export interface Finding {
  advisory: Advisory;
  package: VulnPackage;
  fixedVersions: string[];
  fixState: FixState;
  artifactNames: string[];
  environmentIds: string[];
  firstSeenMs: number | null;
  exemption: VulnExemption | null;
  /** The scope's environment's vuln_gate policy would block it, whatever its mode. */
  blocking: boolean;
}

/** The ListVulnerabilities filters: every one optional, unset means no filter. */
export interface VulnFilters {
  /** That environment's current release, with its exemptions and first-seen times. */
  environmentId?: string;
  /** That release instead of an environment's current one. */
  releaseVersion?: string;
  minSeverity?: MinSeverity;
  /** true: only fixable; false: only not fixable. */
  fixable?: boolean;
  /** true: only KEV-listed; false: only not listed. */
  kev?: boolean;
  includeExempt?: boolean;
}

export interface FindingPage {
  findings: Finding[];
  /** Empty on the last page. */
  nextPageToken: string;
  database: VulnDatabase | null;
}

/** The advisory in one package of one artifact of one release. */
export interface VulnOccurrence {
  releaseId: string;
  releaseVersion: string;
  artifactName: string;
  sbomDigest: string;
  package: VulnPackage;
  fixedVersions: string[];
  fixState: FixState;
  environmentIds: string[];
}

/** When an advisory was first and last seen in an environment, for one package. */
export interface VulnObservation {
  environmentId: string;
  purl: string;
  firstSeenMs: number | null;
  lastSeenMs: number | null;
  /** Null while the environment still contains it. */
  resolvedMs: number | null;
  firstReleaseVersion: string;
}

/** What one promotion's vuln_gate decided about the advisory. */
export interface GateVerdict {
  promotionId: string;
  environmentId: string;
  releaseVersion: string;
  /** "audit" | "warn" | "enforce" (forge's vuln_gate vocabulary). */
  mode: string;
  /** "pass" | "warn" | "block" | "overridden" | "off". */
  verdict: string;
  overrideReason: string;
  createdMs: number | null;
}

export interface VulnerabilityDetail {
  advisory: Advisory;
  occurrences: VulnOccurrence[];
  observations: VulnObservation[];
  exemptions: VulnExemption[];
  /** Newest first. */
  gateVerdicts: GateVerdict[];
}

/** Where an SBOM was produced from (forge release.SBOMRef.Source). */
export type SbomSource = "image" | "registry" | "binary" | "lockfile" | "ci-provided" | (string & {});

/** One artifact of a release, and the SBOM recorded for it, if any. */
export interface ArtifactSbom {
  artifactName: string;
  /** "sha256:<hex>"; empty when no SBOM is recorded (scanState "no-sbom"). */
  sbomDigest: string;
  format: string;
  generator: string;
  source: SbomSource;
  referrer: string;
  componentCount: number;
  scanState: ScanState;
  lastScannedMs: number | null;
  /** When scanState is "failed": the scanner's error, one line. */
  scanError: string;
}

export interface ReleaseVulnerabilities {
  releaseId: string;
  releaseVersion: string;
  artifacts: ArtifactSbom[];
  coverage: VulnCoverage;
  counts: SeverityCounts;
  kev: number;
  fixable: number;
  blocking: number;
  exempt: number;
  findings: Finding[];
  nextPageToken: string;
  database: VulnDatabase | null;
}

/** Every request names its org (null: the caller's default) and the forge project. */
export interface VulnRequestBase {
  orgId: string | null;
  project: string;
}

export interface SummaryRequest extends VulnRequestBase {
  environmentId?: string;
}

export interface ListRequest extends VulnRequestBase {
  filters: VulnFilters;
  pageSize?: number;
  pageToken?: string;
}

export interface DetailRequest extends VulnRequestBase {
  advisoryId: string;
}

export interface ReleaseRequest extends VulnRequestBase {
  version: string;
  environmentId?: string;
  pageSize?: number;
  pageToken?: string;
}
