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
 *                      look like an error. It states the fact, then makes a
 *                      short case for forge and names how to start.
 *   unsupported        names the forge VERSION and forge's own complaint. An
 *                      empty screen here would read as "you have no
 *                      environments", which is the failure mode this exists to
 *                      prevent.
 *   unreachable        UNKNOWN, in the unknown vocabulary — dashed border, muted
 *                      text. Not a red banner: a check that reports a VPN blip
 *                      as a release failure gets switched off in its first week.
 *   malformed          forge produced a document this build could not parse.
 *                      Says so plainly instead of crashing the view.
 */

import { CloudOff, Code2, FileQuestion, PackageOpen, Rocket, ShieldCheck, Wrench } from "lucide-react";
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

/**
 * Why a project would want forge. Each point is backed by something forge
 * actually does — a command, a generator or a check — and none of them
 * carries a number. A claim here that the product does not keep is worse
 * than no pitch at all.
 */
const FORGE_PITCH_POINTS: { icon: LucideIcon; title: string; body: React.ReactNode }[] = [
  {
    icon: Rocket,
    title: "Deploy without the yak-shaving",
    body: (
      <>
        Every environment is declared in the repo.{" "}
        <code className="font-mono text-foreground">forge env deploy</code> records the release,
        applies it and waits until it is healthy. Domains, secrets and promotions are managed here.
      </>
    ),
  },
  {
    icon: Code2,
    title: "Fewer tokens, less guesswork",
    body: (
      <>
        Generated API stubs, ORM, frontend hooks and wiring mean your agent writes the business
        logic, not the boilerplate. Skills and{" "}
        <code className="font-mono text-foreground">forge project</code> introspection hand it the
        project&apos;s shape instead of making it rediscover it file by file.
      </>
    ),
  },
  {
    icon: ShieldCheck,
    title: "Best practices, enforced",
    body: (
      <>
        Built-in skills teach your agent forge&apos;s conventions, and{" "}
        <code className="font-mono text-foreground">forge lint</code> checks them, so drift fails a
        check instead of slipping through to review.
      </>
    ),
  },
];

/**
 * Not a StateShell: this one carries a pitch, and a centered max-w-lg column
 * turns a list into a ragged wall of text. The panel is a surface (bg-card)
 * so the call to action can sit in an inset (bg-background) beneath it —
 * see the elevation rule in components/forge-ui/card.tsx.
 */
export function NotForgeProject({ projectName }: { projectName?: string }) {
  return (
    <section
      data-testid="forge-not-project"
      aria-labelledby="forge-not-project-heading"
      className="mx-auto max-w-2xl space-y-6 rounded-lg border border-border bg-card px-6 py-8"
    >
      <div className="flex items-start gap-3">
        <PackageOpen className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <p data-testid="forge-not-project-fact" className="text-sm text-muted-foreground">
          {projectName ? <span className="font-mono text-foreground">{projectName}</span> : "This project"}{" "}
          has no <span className="font-mono text-foreground">forge.yaml</span> at its root, so there is
          no release ledger or environment topology to show.
        </p>
      </div>

      <div className="space-y-2">
        <h2 id="forge-not-project-heading" className="text-lg font-semibold text-foreground">
          Ship this project with forge
        </h2>
        <p className="text-sm text-muted-foreground">
          forge is the framework built into Reliant. It scaffolds a production-ready app and gives
          your agent the conventions to keep it that way.
        </p>
      </div>

      <ul className="space-y-4">
        {FORGE_PITCH_POINTS.map(({ icon: Icon, title, body }) => (
          <li key={title} className="flex items-start gap-3">
            <Icon className="mt-0.5 h-4 w-4 shrink-0 text-primary" aria-hidden="true" />
            <div className="space-y-1">
              <h3 className="text-sm font-medium text-foreground">{title}</h3>
              <p className="text-sm text-muted-foreground">{body}</p>
            </div>
          </li>
        ))}
      </ul>

      <div
        data-testid="forge-not-project-cta"
        className="space-y-1 rounded-md border border-border/60 bg-background px-4 py-3 text-sm text-muted-foreground"
      >
        <p>
          <span className="font-medium text-foreground">To start,</span> run the{" "}
          <span className="font-medium text-foreground">Forge Migrate</span> workflow in a chat on this
          project. It reads the code, writes a migration plan and waits for your approval before it
          scaffolds anything.
        </p>
        <p>
          Starting from scratch? <code className="font-mono text-foreground">forge project new</code>{" "}
          creates a forge project.
        </p>
      </div>
    </section>
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
