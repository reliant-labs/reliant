// Copyright (c) 2025 Reliant Labs

/**
 * The Security pages' states, each worded for what it means to the reader
 * (vulnerabilities/errors.ts vulnErrorKind) and drawn in the observability
 * states' frame (StateFrame), so they look like every other state in the
 * console.
 *
 * Every state that is not "scanned" says what is unknown and why. None of
 * them, and no empty table, ever reads as "no vulnerabilities": that sentence
 * belongs to VulnNoneKnown alone, shown only when everything in scope was
 * scanned against a current database and nothing was found.
 */

import type { ReactNode } from "react";
import { AlertTriangle, Clock, DatabaseZap, FileQuestion, Lock, RefreshCw, SearchX, ShieldCheck, ShieldQuestion } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Link from "@/components/forge-ui/link";
import { obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { vulnErrorKind, type VulnErrorKind } from "@/services/controlPlane/vulnerabilities/errors";
import type { ArtifactSbom, EnvVulnSummary, VulnDatabase } from "@/services/controlPlane/vulnerabilities/types";

import { GettingStarted } from "../GettingStarted";
import { formatAgo, formatWhen } from "../Observability/format";
import { StateFrame } from "../Observability/ObsStates";
import { VULN_DOCS_URL } from "./vulnModel";

const code = (text: string) => <code className="font-mono text-ink">{text}</code>;

const RETRY_BUTTON = "min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0";

function when(ms: number | null, now: number): string {
  return ms === null ? "an unknown time" : `${formatAgo(ms, now)} (${formatWhen(ms, now)})`;
}

const KIND_ICON: Record<VulnErrorKind, ReactNode> = {
  "not-configured": <ShieldQuestion className="h-4 w-4" />,
  denied: <Lock className="h-4 w-4" />,
  "not-found": <SearchX className="h-4 w-4" />,
  "bad-token": <AlertTriangle className="h-4 w-4" />,
  unavailable: <AlertTriangle className="h-4 w-4" />,
};

/** What a failed read means, in words: a title and the sentence under it (VulnError, the Overview card). */
export function vulnErrorCopy(kind: VulnErrorKind, subject: string): { title: string; body: ReactNode } {
  switch (kind) {
    case "not-configured":
      return {
        title: "This control plane doesn't report vulnerabilities yet",
        body: "It has no vulnerability service, or runs without the findings store behind it. Nothing here means this project is clean: its findings are unknown until the control plane is upgraded.",
      };
    case "denied":
      return {
        title: "You can't read this project's vulnerabilities",
        body: (
          <>
            Vulnerability findings need the {code("vuln:read")} grant in this organization, or for the environment. No deploy grant implies
            it: a list of exploitable weaknesses is an attack map. An organization admin can give it to you.
          </>
        ),
      };
    case "not-found":
      return { title: `${subject} isn't in this project`, body: "No environment you can read and no recorded release of this project has it." };
    case "bad-token":
      return { title: "That page of findings can't be opened here", body: "A page link belongs to one set of filters. Start from the first page." };
    case "unavailable":
      return { title: `Couldn't read ${subject}`, body: "The control plane did not answer." };
  }
}

/** A failed read, in words. `subject` names what was being read: "the vulnerabilities", "CVE-2026-1234". */
export function VulnError({ error, subject, onRetry, retrying, testId = "vuln-error" }: { error: unknown; subject: string; onRetry?: () => void; retrying?: boolean; testId?: string }) {
  const kind = vulnErrorKind(error);
  const copy = vulnErrorCopy(kind, subject);
  const canRetry = onRetry && (kind === "unavailable" || kind === "bad-token");
  return (
    <div data-testid={testId} data-kind={kind}>
      <StateFrame
        testId={`${testId}-${kind}`}
        tone={kind === "not-configured" || kind === "not-found" ? "neutral" : "error"}
        icon={KIND_ICON[kind]}
        title={copy.title}
        actions={
          canRetry ? (
            <Button variant="outline" size="sm" className={RETRY_BUTTON} onClick={onRetry} loading={retrying} disabled={retrying} leftIcon={<RefreshCw className="h-3 w-3" />}>
              Try again
            </Button>
          ) : undefined
        }
      >
        <p>{copy.body}</p>
        {kind === "unavailable" && obsErrorMessage(error) && <p className="break-words font-mono text-meta text-ink">{obsErrorMessage(error)}</p>}
      </StateFrame>
    </div>
  );
}

/**
 * The scope has nothing scanned to show: nothing deployed, releases cut
 * before SBOMs, or first scans still running. What the page will show, why it
 * is empty, and how SBOMs get made — never a blank page or "no
 * vulnerabilities".
 */
export function VulnNotScannedYet({ envs, children }: { envs: readonly EnvVulnSummary[]; children?: ReactNode }) {
  const deployed = envs.filter((env) => env.releaseId);
  const allPending = deployed.length > 0 && deployed.every((env) => env.scanState === "pending");
  const deployEnv = deployed[0]?.environmentName ?? "<env>";
  const about =
    "This page lists the known vulnerabilities in what each environment runs, from the SBOM (software bill of materials) forge records for every artifact of a release, scanned by the control plane and rescanned daily.";
  if (allPending) {
    return (
      <GettingStarted testId="vuln-scan-pending" icon={<Clock className="h-4 w-4" />} title="Scan pending" about={about} docs={{ href: VULN_DOCS_URL, label: "How SBOMs and vulnerability scans work" }}>
        <p>
          The SBOMs of {deployed.map((env) => `${env.environmentName}'s ${env.releaseVersion}`).join(" and ")} are recorded and their first scan has not finished.
          Findings appear here when it does, usually within minutes.
        </p>
        {children}
      </GettingStarted>
    );
  }
  return (
    <GettingStarted
      testId="vuln-not-scanned"
      icon={<FileQuestion className="h-4 w-4" />}
      title="Not scanned yet"
      about={about}
      steps={[
        {
          text: deployed.length
            ? "forge generates the SBOMs when it cuts a release. Deploy again with a current forge; the scan follows within minutes."
            : "forge generates the SBOMs when it cuts a release. Deploy an environment through Reliant; the scan follows within minutes.",
          command: `reliant forge env deploy ${deployEnv}`,
        },
      ]}
      docs={{ href: VULN_DOCS_URL, label: "How SBOMs and vulnerability scans work" }}
    >
      <p>
        {deployed.length
          ? "No release running in these environments has a scanned SBOM yet, so their vulnerabilities are unknown, not absent."
          : "Nothing has been deployed to these environments, so there is nothing to scan yet."}
      </p>
      {children}
    </GettingStarted>
  );
}

/** Scanned, all of it, against a current database, and nothing found. The only "clean" sentence on these pages. */
export function VulnNoneKnown({ database, envs, subject }: { database: VulnDatabase | null; envs: readonly EnvVulnSummary[]; subject?: string }) {
  const now = Date.now();
  const deployed = envs.filter((env) => env.releaseId);
  const what = subject ?? (deployed.map((env) => `${env.environmentName}'s ${env.releaseVersion}`).join(" and ") || "the releases in scope");
  return (
    <StateFrame testId="vuln-none-known" icon={<ShieldCheck className="h-4 w-4" />} title="No known vulnerabilities">
      <p>
        Every artifact of {what} has an SBOM, and its newest scan found
        nothing{database?.builtAtMs ? `, against a vulnerability database built ${formatAgo(database.builtAtMs, now)}` : ""}.
      </p>
      <p>Known means published: the daily rescan picks up advisories as they appear.</p>
    </StateFrame>
  );
}

/** One environment whose findings fall short of a current, complete scan, and why. */
export function ScanStateNote({
  env,
  database,
  artifacts,
  sbomsHref,
}: {
  env: EnvVulnSummary;
  database: VulnDatabase | null;
  /** The release's artifacts, when read: they carry the failing scan's reason. */
  artifacts?: readonly ArtifactSbom[];
  sbomsHref?: string;
}) {
  const now = Date.now();
  const name = env.environmentName;
  const release = <span className="font-mono text-ink">{env.releaseVersion}</span>;
  const sboms = sbomsHref ? (
    <Link href={sbomsHref} className="font-medium text-ink underline-offset-2 hover:underline">
      See its SBOMs
    </Link>
  ) : null;
  const testId = `vuln-scan-${env.scanState}-${name}`;
  switch (env.scanState) {
    case "no-sbom": {
      const missing = env.coverage.artifacts - env.coverage.withSbom;
      return (
        <StateFrame testId={testId} icon={<FileQuestion className="h-4 w-4" />} title={`${name}: not scanned`}>
          <p>
            {missing} of {env.coverage.artifacts} artifacts in {release} have no SBOM, so their vulnerabilities are unknown, not absent. They were built
            by a forge that predates SBOMs, or their SBOM could not be produced; the next release cut makes them. {sboms}
          </p>
        </StateFrame>
      );
    }
    case "pending":
      return (
        <StateFrame testId={testId} icon={<Clock className="h-4 w-4" />} title={`${name}: scan pending`}>
          <p>
            The SBOMs of {release} are recorded and their first scan has not finished. Findings appear when it does. {sboms}
          </p>
        </StateFrame>
      );
    case "failed": {
      const reasons = [...new Set((artifacts ?? []).filter((a) => a.scanState === "failed" && a.scanError).map((a) => `${a.artifactName}: ${a.scanError}`))];
      return (
        <StateFrame testId={testId} tone="error" icon={<AlertTriangle className="h-4 w-4" />} title={`${name}: scan failed`}>
          <p>
            Scans of {release} have failed since {when(env.failingSinceMs, now)}.{" "}
            {env.lastScannedMs !== null
              ? `The findings shown are from the last successful scan, ${formatAgo(env.lastScannedMs, now)}.`
              : "It has never been scanned successfully, so its vulnerabilities are unknown."}{" "}
            {sboms}
          </p>
          {reasons.map((reason) => (
            <p key={reason} className="break-words font-mono text-meta text-ink" data-testid="vuln-scan-error">
              {reason}
            </p>
          ))}
        </StateFrame>
      );
    }
    case "stale":
      return (
        <StateFrame testId={testId} icon={<DatabaseZap className="h-4 w-4" />} title={`${name}: stale vulnerability database`}>
          <p>
            The newest scan of {release} used a database built {database?.builtAtMs ? formatAgo(database.builtAtMs, now) : "too long ago"}, past its freshness
            bound, so advisories published since are missing. This is not a clean bill; the next rescan with a fresh database replaces it.
          </p>
        </StateFrame>
      );
    default:
      return null;
  }
}
