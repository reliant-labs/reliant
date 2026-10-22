// Copyright (c) 2025 Reliant Labs

/**
 * One-line notices for a SOURCE that could not answer — the daemon, or the
 * control plane — rendered beside whatever the other source did answer.
 *
 * These exist because the forge screens now join two sources, and the failure
 * of one must degrade the screen, never blank it. So each is a quiet strip,
 * not a full-panel state: a hosted environment stays on screen, fully
 * rendered, with a sentence under the header saying the daemon is offline and
 * which parts of the page that affects.
 *
 * Neither is styled as an error unless it is one. A daemon that is asleep is
 * a normal state for a laptop; an organization without the deploy product is
 * a normal state for most users.
 */

import { CloudOff, Info } from "lucide-react";

import type { CloudAvailability } from "@/services/forge/cloudEnvs";
import type { DaemonSide } from "@/services/forge/environments";

function Strip({
  testId,
  children,
  tone = "quiet",
}: {
  testId: string;
  children: React.ReactNode;
  tone?: "quiet" | "problem";
}) {
  const Icon = tone === "problem" ? CloudOff : Info;
  return (
    <div
      data-testid={testId}
      className="flex items-start gap-2 rounded-lg border border-dashed border-border px-3 py-2 text-xs text-muted-foreground"
    >
      <Icon
        className={tone === "problem" ? "mt-px h-3.5 w-3.5 shrink-0 text-warning" : "mt-px h-3.5 w-3.5 shrink-0"}
        aria-hidden="true"
      />
      <div className="min-w-0 space-y-0.5">{children}</div>
    </div>
  );
}

/**
 * The daemon did not answer. `scope` names what that costs on THIS screen,
 * because "daemon offline" alone reads as "nothing here is true".
 */
export function DaemonOfflineNotice({
  scope,
  detail,
}: {
  scope: string;
  detail?: string;
}) {
  return (
    <Strip testId="forge-daemon-offline" tone="problem">
      <p>
        <span className="font-medium text-foreground">Your daemon is offline.</span> {scope}
      </p>
      {detail && <p className="font-mono text-2xs">{detail}</p>}
    </Strip>
  );
}

/**
 * Why this project's environments are not on screen. Returns null for
 * `available` and for `no-control-plane` — the latter is a local/OSS build,
 * where "no cloud environments" is simply true and needs no sentence.
 *
 * ── THE COPY RULE (#366) ────────────────────────────────────────────────────
 *
 * NO INTERNAL NOUNS. These sentences used to name "the control plane", which
 * is OUR infrastructure: the customer did not choose it, cannot visit it, and
 * cannot act on its state. Telling them it is unreachable spends the only line
 * on the screen explaining our architecture to someone who wanted to know why
 * their environments are missing.
 *
 * So each case says what is true FOR THEM, in their own nouns: what they
 * cannot see, and what (if anything) they can do about it. The server's own
 * message is still shown as `detail` for a support conversation — it is the
 * one place a technical string is worth more than a plain one.
 */
export function CloudNotice({
  availability,
  detail,
}: {
  availability: CloudAvailability | undefined;
  detail?: string;
}) {
  switch (availability) {
    case "no-access":
      return (
        <Strip testId="forge-cloud-no-access">
          <p>
            Your role in this organization can&apos;t see its environments. Ask an organization
            admin for access.
          </p>
          {detail && <p className="font-mono text-2xs">{detail}</p>}
        </Strip>
      );
    case "not-configured":
      return (
        <Strip testId="forge-cloud-not-configured">
          <p>Deploying isn&apos;t available on this installation.</p>
        </Strip>
      );
    case "unreachable":
      return (
        <Strip testId="forge-cloud-unreachable" tone="problem">
          <p>
            <span className="font-medium text-foreground">
              Couldn&apos;t load this project&apos;s environments.
            </span>{" "}
            They haven&apos;t gone anywhere — try again in a moment.
          </p>
          {detail && <p className="font-mono text-2xs">{detail}</p>}
        </Strip>
      );
    default:
      return null;
  }
}

/** Which daemon states are forge's own answers — rendered by ForgeStates — rather than an outage. */
export function isDaemonAnswer(side: DaemonSide): boolean {
  return side === "not-forge-project" || side === "unsupported" || side === "malformed" || side === "unreachable";
}
