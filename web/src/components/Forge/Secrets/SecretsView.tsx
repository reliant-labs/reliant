// Copyright (c) 2025 Reliant Labs

/**
 * The secrets screen's presentation layer.
 *
 * PURE PROPS, the same split TopologyView uses: it takes an outcome and
 * callbacks and owns no fetching, which is what lets the visual contract be
 * tested by handing it a report object instead of standing up a query client and
 * a transport.
 *
 * The four non-report outcomes are rendered by the SAME components the topology
 * screen uses (../ForgeStates) rather than by local copies. "Not a forge
 * project" and "your forge is too old" mean exactly the same thing on both
 * screens, and two wordings would let them drift.
 *
 * Nothing in this file stringifies the report. No response body reaches an error
 * message, a log line, or a telemetry path — there is no value in the document to
 * leak, and keeping it that way by construction is cheaper than auditing it
 * later.
 */

import { cn } from "@/lib/utils";
import type { ForgeOutcome, ForgeTopologyReport } from "@/services/forge/topology";
import type { ForgeSecretsReport } from "@/services/forge/secrets";
import { providerKind, secretEntries } from "@/services/forge/secrets";
import { environments } from "@/services/forge/topology";

import {
  ForgeMalformed,
  ForgeUnreachable,
  ForgeUnsupported,
  NotForgeProject,
} from "../ForgeStates";
import { EnvSelector } from "./EnvSelector";
import { InertKeys } from "./InertKeys";
import { SecretRow } from "./SecretRow";
import { StoreSummary } from "./StoreSummary";

export interface SecretsViewProps {
  /** The secret report for the selected env. */
  outcome: ForgeOutcome<ForgeSecretsReport> | undefined;
  isLoading: boolean;
  /** A transport/daemon failure — genuinely an error, unlike every outcome above. */
  error?: Error | null;
  /** Topology, used ONLY to enumerate the environments. Never hardcode a list. */
  topology: ForgeOutcome<ForgeTopologyReport> | undefined;
  topologyLoading?: boolean;
  selectedEnv: string | null;
  onSelectEnv: (env: string) => void;
  projectName?: string;
}

export function SecretsView({
  outcome,
  isLoading,
  error,
  topology,
  topologyLoading,
  selectedEnv,
  onSelectEnv,
  projectName,
}: SecretsViewProps) {
  const envNames =
    topology?.kind === "report" ? environments(topology.report).map((env) => env.env) : [];

  // The project-level outcomes are answered by whichever report arrived first.
  // Both RPCs resolve the same project, so "not a forge project" from either is
  // the same fact and should not wait for the other.
  const projectLevel = outcome ?? topology;
  if (projectLevel?.kind === "not-forge-project") return <NotForgeProject projectName={projectName} />;
  if (projectLevel?.kind === "unsupported") return <ForgeUnsupported meta={projectLevel.meta} />;

  const header = (
    <header className="space-y-2">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h1 className="text-lg font-medium text-foreground">
          {projectName ? `${projectName} secrets` : "Secrets"}
        </h1>
        {selectedEnv && (
          <span className="font-mono text-xs text-muted-foreground">{selectedEnv}</span>
        )}
      </div>
      {/* Stated once, at the top, because it is the question every reader has. */}
      <p className="text-xs text-muted-foreground">
        This screen shows <span className="text-foreground">presence only</span>. Secret values never
        leave the daemon and are not in the response that built this page — there is no value here to
        reveal, copy, or unmask.
      </p>
      <EnvSelector
        envs={envNames}
        selected={selectedEnv}
        onSelect={onSelectEnv}
        isLoading={topologyLoading}
      />
    </header>
  );

  let body: React.ReactNode;

  if (!selectedEnv) {
    body =
      topologyLoading || envNames.length > 0 ? (
        <Panel testId="secrets-no-env">Select an environment to see which secrets it declares.</Panel>
      ) : (
        <Panel testId="secrets-no-envs">This forge project declares no environments yet.</Panel>
      );
  } else if (isLoading && !outcome) {
    body = <Panel testId="secrets-loading">Reading which secrets {selectedEnv} declares…</Panel>;
  } else if (error && !outcome) {
    body = (
      // The daemon could not be reached. Note what is NOT here: the response
      // body. Only the transport error's own message, which never carried one.
      <div
        data-testid="secrets-error"
        className="rounded-lg border border-destructive/40 bg-destructive/10 px-6 py-10 text-center text-sm text-destructive"
      >
        Could not reach your daemon to read this environment&apos;s secrets: {error.message}
      </div>
    );
  } else if (!outcome) {
    body = null;
  } else if (outcome.kind === "unreachable") {
    body = <ForgeUnreachable meta={outcome.meta} />;
  } else if (outcome.kind === "malformed") {
    body = <ForgeMalformed meta={outcome.meta} />;
  } else if (outcome.kind === "not-forge-project" || outcome.kind === "unsupported") {
    // Already handled above; narrowed here so the report branch is exhaustive.
    body = null;
  } else {
    body = <SecretsReport report={outcome.report} />;
  }

  return (
    <div className="space-y-5" data-testid="forge-secrets">
      {header}
      {body}
    </div>
  );
}

function SecretsReport({ report }: { report: ForgeSecretsReport }) {
  const entries = secretEntries(report);
  const kind = providerKind(report);

  return (
    <div className="space-y-5">
      <StoreSummary report={report} />

      {entries.length === 0 ? (
        <Panel testId="secrets-none-declared">
          No workload in this environment declares a secret. Nothing needs to be set for{" "}
          <span className="font-mono">forge env up</span> to start it.
        </Panel>
      ) : (
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full border-collapse text-sm">
            <caption className="sr-only">
              Declared secrets for this environment, by presence and declaring workload. No values are
              shown or fetched.
            </caption>
            <thead>
              <tr className="border-b border-border">
                <th scope="col" className="px-3 py-2 text-left text-xs font-medium text-muted-foreground">
                  Secret
                </th>
                <th scope="col" className="px-3 py-2 text-left text-xs font-medium text-muted-foreground">
                  {/* Not "Value". There is no value column and never will be. */}
                  Presence
                </th>
                <th scope="col" className="px-3 py-2 text-left text-xs font-medium text-muted-foreground">
                  Declared by
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <SecretRow key={entry.name} entry={entry} report={report} />
              ))}
            </tbody>
          </table>
        </div>
      )}

      <InertKeys report={report} />

      {kind === "file" && (
        <p className="text-2xs text-muted-foreground">
          Presence is read from the store&apos;s keys. Reliant is told a key exists; it is never told
          what the key holds.
        </p>
      )}
    </div>
  );
}

function Panel({ testId, children }: { testId: string; children: React.ReactNode }) {
  return (
    <div
      data-testid={testId}
      className={cn(
        "rounded-lg border border-dashed border-border px-6 py-10 text-center text-sm text-muted-foreground"
      )}
    >
      {children}
    </div>
  );
}
