// Copyright (c) 2025 Reliant Labs

/**
 * The non-report outcomes, each rendered as ITSELF.
 *
 * All four of these arrive as successful RPCs carrying data — the backend is
 * explicit that "no forge.yaml", "your forge is too old" and "the cluster was
 * unreachable" are answers, not failures. The UI has to keep them apart:
 *
 *   not-forge-project  informational. Most reliant projects are not forge
 *                      projects, so this is the expected answer and must not
 *                      look like an error.
 *   unsupported        names the forge VERSION and forge's own complaint. An
 *                      empty screen here would read as "you have no
 *                      environments", which is the failure mode this exists to
 *                      prevent.
 *   cannot-render      the daemon is REACHABLE and its forge cannot render any
 *                      environment (a CGO-free build has no kcl_plugin.forge).
 *                      Kept apart from `unsupported` because that is fixed by
 *                      upgrading and this is not, and kept apart from a
 *                      transport error because this condition used to surface
 *                      as "Could not reach your daemon" — which blamed the
 *                      network for a build flag and sent people nowhere useful.
 *   unreachable        UNKNOWN, in the unknown vocabulary — dashed border, muted
 *                      text. Not a red banner: a check that reports a VPN blip
 *                      as a release failure gets switched off in its first week.
 *   malformed          forge produced a document this build could not parse.
 *                      Says so plainly instead of crashing the view.
 */

import { CloudOff, FileQuestion, PackageOpen, Wrench } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

interface ShellProps {
  icon: LucideIcon;
  title: string;
  children: React.ReactNode;
  testId: string;
  /** Dashed when the state is UNKNOWN rather than merely informational. */
  unknown?: boolean;
}

function StateShell({ icon: Icon, title, children, testId, unknown }: ShellProps) {
  return (
    <div
      data-testid={testId}
      className={cn(
        "mx-auto flex max-w-lg flex-col items-center gap-3 rounded-lg px-6 py-12 text-center",
        unknown ? "border border-dashed border-border" : "border border-border"
      )}
    >
      <Icon className="h-8 w-8 text-muted-foreground" aria-hidden="true" />
      <h2 className="text-base font-medium text-foreground">{title}</h2>
      <div className="space-y-2 text-sm text-muted-foreground">{children}</div>
    </div>
  );
}

export function NotForgeProject({ projectName }: { projectName?: string }) {
  return (
    <StateShell icon={PackageOpen} title="Not a forge project" testId="forge-not-project">
      <p>
        {projectName ? <span className="font-mono">{projectName}</span> : "This project"} has no{" "}
        <span className="font-mono">forge.yaml</span> at its root, so there is no release ledger or
        environment topology to show.
      </p>
      <p>This is expected — most projects are not forge projects.</p>
    </StateShell>
  );
}

/**
 * The daemon is REACHABLE and its forge simply cannot render any environment.
 *
 * This state exists because the condition was previously displayed as "Could
 * not reach your daemon", which blames the network for a build flag. On every
 * managed cloud workspace the daemon was connected and healthy; its embedded
 * forge had been compiled without CGO, so forge's kcl_plugin.forge namespace
 * was never registered and every render was refused. A user following the old
 * message would check their connection, and nothing they found there could
 * explain it.
 *
 * Distinct from `unsupported` on purpose. That one means "this forge is too
 * OLD for the command this screen needs" and is fixed by upgrading; this one
 * means "this forge cannot render AT ALL, at any version" and is fixed by
 * rebuilding/reinstalling it. Collapsing them would send people to a version
 * bump that cannot help.
 *
 * `reason` and `detail` are forge's OWN words, forwarded from its doctor
 * report, so this panel and `forge doctor` never tell different stories.
 */
export function ForgeCannotRender({
  forgeVersion,
  reason,
  detail,
}: {
  forgeVersion?: string;
  reason?: string;
  detail?: string;
}) {
  return (
    <StateShell
      icon={Wrench}
      title="This machine's forge can't render environments"
      testId="forge-cannot-render"
    >
      <p>
        Your daemon is reachable, but the forge on it —{" "}
        <span className="font-mono text-foreground">{forgeVersion || "an unknown version"}</span> —
        cannot render, deploy or bring up any environment.
      </p>
      {reason && <p className="font-mono text-xs">{reason}</p>}
      {detail && (
        <pre className="whitespace-pre-wrap break-words text-left font-mono text-xs text-muted-foreground">
          {detail}
        </pre>
      )}
      <p>
        Nothing is wrong with this project or your connection — the forge binary itself is missing
        the KCL plugin it renders through, which usually means it was built without CGO.
      </p>
    </StateShell>
  );
}

export function ForgeUnsupported({ meta }: { meta: ForgeReportMeta }) {
  return (
    <StateShell icon={Wrench} title="This forge is too old for the topology view" testId="forge-unsupported">
      <p>
        The forge on your daemon is{" "}
        <span className="font-mono text-foreground">{meta.forgeVersion || "an unknown version"}</span>
        , which does not support the command this screen needs.
      </p>
      {meta.unsupportedReason && (
        <p className="font-mono text-xs">{meta.unsupportedReason}</p>
      )}
      <p>
        Your environments have not gone away — this build simply cannot read them. Upgrade forge on
        the daemon to see the topology.
      </p>
    </StateShell>
  );
}

export function ForgeUnreachable({ meta }: { meta: ForgeReportMeta }) {
  return (
    <StateShell icon={CloudOff} title="Live state is unknown" testId="forge-unreachable" unknown>
      <p>
        The cluster could not be read, so nothing here can be reported as either healthy or drifted.
      </p>
      {meta.unreachableReason && <p className="font-mono text-xs">{meta.unreachableReason}</p>}
      <p>
        This is <span className="text-foreground">not</span> evidence of a problem with any release.
        It means the measurement did not happen.
      </p>
    </StateShell>
  );
}

export function ForgeMalformed({ meta }: { meta: ForgeReportMeta }) {
  return (
    <StateShell icon={FileQuestion} title="Could not read forge's report" testId="forge-malformed" unknown>
      <p>
        forge {meta.forgeVersion ? <span className="font-mono">{meta.forgeVersion}</span> : null}{" "}
        returned a document this build of reliant could not parse, so the topology cannot be shown.
      </p>
      <p>Nothing below should be read as a statement about your environments.</p>
    </StateShell>
  );
}
