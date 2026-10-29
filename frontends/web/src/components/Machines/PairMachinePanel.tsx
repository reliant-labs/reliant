import { useEffect, useRef, useState } from "react";
import { AuthorizeOrgPicker, useAuthorizeOrg } from "@/components/Orgs/AuthorizeOrgPicker";
import { getActiveOrgId } from "@/store/activeOrgStore";
import { Check, Download, Loader2, Monitor, Terminal } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { cn } from "@/lib/utils";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useEventBus } from "@/lib/event-context";
import { getControlPlaneURL } from "@/lib/constants";
import { machineDisplayName } from "@/lib/machineName";
import {
  daemonStartCommand,
  daemonStartCommandNeedsEditing,
  GATEWAY_URL_PLACEHOLDER,
} from "@/lib/cli-commands";
import {
  decidePendingMachine,
  formatPairingCode,
  lookupPendingMachine,
  normalizePairingCode,
  PairingError,
  type PendingMachine,
} from "@/services/controlPlane/pairing";
import {
  describeTerminal,
  ReliantDownloadOptions,
  supportsHomebrewCask,
  useDetectedOS,
} from "@/components/ReliantDownloadOptions";

/**
 * Why the caller shows the panel, which decides what counts as "connected":
 *
 * - "bootstrap" — the caller is blocked until SOME machine exists
 *   (onboarding's compute step). Any connected machine finishes the job.
 * - "add" — the caller is adding ANOTHER machine and waits for it (the Add
 *   machine sheet). Only a machine that was not in the list when the panel
 *   opened counts: the one already connected is not the one being set up.
 * - "reference" — standing instructions (Settings → Machines). Pairing still
 *   works from here, but nothing waits and nothing is reported up.
 */
export type PairMachineMode = "bootstrap" | "add" | "reference";

interface PairMachinePanelProps {
  /** Fired once, with the machine's id, when the awaited machine connects. */
  onConnected?: (daemonId: string) => void;
  /** See PairMachineMode. Defaults to "add". */
  mode?: PairMachineMode;
  /** A code to start from: the CLI's printed link carries one. */
  initialCode?: string;
}

function StepHeading({ index, title, tone = "neutral" }: { index: number; title: string; tone?: "sky" | "neutral" }) {
  return (
    <div className="flex items-center gap-2">
      <span
        className={cn(
          "flex h-5 w-5 flex-shrink-0 items-center justify-center rounded-full text-2xs font-semibold",
          tone === "sky" ? "bg-sky-500/20 text-sky-700 dark:text-sky-300" : "bg-muted text-muted-foreground",
        )}
      >
        {index}
      </span>
      <span className="text-xs font-medium text-foreground">{title}</span>
    </div>
  );
}

type Approval =
  | { kind: "enter" }
  | { kind: "looking" }
  | { kind: "confirm"; machine: PendingMachine }
  | { kind: "deciding"; machine: PendingMachine; approved: boolean }
  | { kind: "approved"; machine: PendingMachine }
  | { kind: "declined" };

/**
 * Connect your own computer — a laptop or a server — by PAIRING it.
 *
 * `reliant daemon start` on that machine prints a short code. Typed in here,
 * the code turns this panel into the approval: "Approve machine
 * <hostname>?". The CLI was waiting for exactly that answer, so it connects,
 * and the panel sees a new machine arrive in the registry.
 *
 * This replaced generate-a-token, copy it, paste it into a command, paste it
 * again at the command's prompt. Nothing secret crosses the clipboard now:
 * the code is useless without a signed-in person approving it here, and the
 * credential goes straight from the control plane to the CLI.
 *
 * The CLI also prints a link that approves the same code; whichever the
 * person uses, the machine connecting is what this panel waits for.
 */
export function PairMachinePanel({ onConnected, mode = "add", initialCode = "" }: PairMachinePanelProps) {
  const { activeDaemon, daemons, loading: daemonLoading } = useDaemonStatus();
  const detectedOS = useDetectedOS();
  const events = useEventBus();
  const notifiedRef = useRef(false);
  const [code, setCode] = useState(formatPairingCode(initialCode));
  const [approval, setApproval] = useState<Approval>({ kind: "enter" });
  const [error, setError] = useState<string | null>(null);
  // The org the machine being approved will act in. The active org is the starting point.
  const authorizeOrg = useAuthorizeOrg(getActiveOrgId());

  // The machines that existed when the panel opened. In "add" mode none of
  // them is the one the user is setting up.
  const knownIdsRef = useRef<Set<string> | null>(null);
  if (knownIdsRef.current === null && !daemonLoading) {
    knownIdsRef.current = new Set(daemons.map((d) => d.daemonId));
  }
  const known = knownIdsRef.current;
  const connected =
    mode === "add"
      ? known
        ? daemons.find((d) => d.status === DaemonStatus.ACTIVE && !known.has(d.daemonId))
        : undefined
      : mode === "bootstrap"
        ? activeDaemon
        : undefined;

  useEffect(() => {
    if (!connected || notifiedRef.current) return;
    notifiedRef.current = true;
    onConnected?.(connected.daemonId);
  }, [connected, onConnected]);

  // ── Electron: is the `reliant` CLI on PATH on this machine? ──────────────
  const isElectron = typeof window !== "undefined" && !!window.electronAPI;
  const [cliInstalled, setCliInstalled] = useState<boolean | null>(null);
  const [cliPath, setCliPath] = useState<string | null>(null);
  const [installingCli, setInstallingCli] = useState(false);
  useEffect(() => {
    if (!isElectron || !window.electronAPI?.getCliStatus) return;
    let cancelled = false;
    window.electronAPI
      .getCliStatus()
      .then((status) => {
        if (cancelled) return;
        setCliInstalled(status.installed);
        setCliPath(status.path);
      })
      .catch(() => {
        if (!cancelled) setCliInstalled(false);
      });
    return () => {
      cancelled = true;
    };
  }, [isElectron]);

  const handleInstallCli = async () => {
    if (!window.electronAPI?.installCLI) return;
    setInstallingCli(true);
    setError(null);
    try {
      const result = await window.electronAPI.installCLI();
      if (result.success) {
        const status = await window.electronAPI.getCliStatus?.();
        setCliInstalled(status ? status.installed : true);
        setCliPath(status?.path ?? null);
        events.emit("toast:show", { message: result.message || "CLI installed", variant: "success" });
      } else {
        setError(result.error || "Failed to install the CLI");
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to install the CLI");
    } finally {
      setInstallingCli(false);
    }
  };

  const lookUp = async () => {
    setError(null);
    setApproval({ kind: "looking" });
    try {
      const machine = await lookupPendingMachine(code);
      if (machine.kind === "cli_login") {
        // This sheet waits for a machine to connect. A CLI sign-in's code is
        // approved on the page its terminal linked, which says what it is.
        setApproval({ kind: "enter" });
        setError("That code signs in the Reliant CLI, not a machine. Open the link your terminal printed to approve it.");
        return;
      }
      setApproval({ kind: "confirm", machine });
    } catch (err) {
      setApproval({ kind: "enter" });
      setError(
        err instanceof PairingError && err.notFound
          ? "No machine is waiting with that code. Check it against your terminal — a code lasts ten minutes."
          : err instanceof Error
            ? err.message
            : "Could not look up the code",
      );
    }
  };

  const decide = async (machine: PendingMachine, approved: boolean) => {
    setError(null);
    setApproval({ kind: "deciding", machine, approved });
    try {
      await decidePendingMachine(machine.userCode, approved, approved ? authorizeOrg.orgId : null);
      setApproval(approved ? { kind: "approved", machine } : { kind: "declined" });
    } catch (err) {
      setApproval({ kind: "confirm", machine });
      setError(err instanceof Error ? err.message : "Could not record your answer");
    }
  };

  // Is Reliant already installed SOMEWHERE? Folds (never removes) step 1.
  const installLikelyDone = cliInstalled === true || isElectron || daemons.length > 0;
  const installEvidenceNote =
    cliInstalled === true
      ? "The reliant CLI is already installed on this machine."
      : isElectron
        ? "You are running the Reliant desktop app, so it is already installed here."
        : daemons.length > 0
          ? "You already have a machine connected. Open this if you are setting up another one."
          : undefined;
  const terminal = describeTerminal(detectedOS);

  if (connected) {
    return (
      <div className="flex items-start gap-3 rounded-xl border border-emerald-500/30 bg-emerald-500/5 p-4" role="status">
        <Check className="mt-0.5 h-4 w-4 text-emerald-500" />
        <div>
          <h3 className="text-sm font-medium text-foreground">{machineDisplayName(connected)} is connected</h3>
          <p className="mt-0.5 text-xs text-muted-foreground">Your projects can run on it now.</p>
        </div>
      </div>
    );
  }

  const normalized = normalizePairingCode(code);

  return (
    <div className="space-y-4">
      <section className="space-y-3 rounded-xl border border-sky-500/30 bg-sky-500/5 p-4">
        <StepHeading index={1} title="Install Reliant on that machine" tone="sky" />
        <p className="text-xs leading-relaxed text-muted-foreground">
          <span className="font-medium text-foreground">Using the desktop app on this computer?</span> It already runs
          Reliant here and connects on its own — no terminal needed.
        </p>
        <ReliantDownloadOptions defaultCollapsed={installLikelyDone} collapsedNote={installEvidenceNote} />
      </section>

      <section className="space-y-4 rounded-xl border border-border/60 bg-background p-4">
        <div className="space-y-1.5">
          <StepHeading index={2} title={`Run this in ${terminal.name} on that machine`} />
          <p className="text-xs leading-relaxed text-muted-foreground">
            {terminal.howToOpen} Then paste this in and press <span className="font-medium text-foreground">Enter</span>.
            It prints a pairing code.
          </p>
          {isElectron && cliInstalled === false && (
            <div className="space-y-2 rounded border border-amber-500/30 bg-amber-500/5 p-2.5">
              <p className="flex items-start gap-2 text-xs text-amber-700 dark:text-amber-300">
                <Terminal className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" />
                <span>
                  The <code className="font-mono">reliant</code> command is not on your PATH yet.
                  {supportsHomebrewCask(detectedOS) ? " Install it here, or with the Homebrew cask." : " Install it here."}
                </span>
              </p>
              <Button size="sm" variant="outline" onClick={() => void handleInstallCli()} loading={installingCli}>
                {installingCli ? "Installing…" : "Install reliant CLI"}
              </Button>
            </div>
          )}
          {isElectron && cliInstalled === true && cliPath && (
            <p className="flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
              <Check className="h-3 w-3" />
              <span>
                <code className="font-mono">reliant</code> CLI installed at <code className="font-mono">{cliPath}</code>
              </span>
            </p>
          )}
          <code className="block select-all break-all rounded border border-border/40 bg-background px-3 py-2 font-mono text-xs text-foreground">
            {daemonStartCommand()}
          </code>
          {daemonStartCommandNeedsEditing() && (
            <p className="text-xs text-yellow-600 dark:text-yellow-400">
              Replace {GATEWAY_URL_PLACEHOLDER} with your daemon-gateway address before running this. It is a separate
              process from the API server, so the machine cannot infer it on localhost.
            </p>
          )}
        </div>

        <div className="space-y-2 border-t border-border/50 pt-4">
          <StepHeading index={3} title="Approve it here" />
          {!getControlPlaneURL() ? (
            // A deployment with no Reliant control plane has nothing to pair
            // through: the machine connects with an access token instead.
            <p className="text-xs leading-relaxed text-muted-foreground" data-testid="pair-token-fallback">
              This deployment has no Reliant control plane to pair through. Create an access token under Settings →
              Access tokens, then run <code className="font-mono">reliant daemon start --token</code> and paste it.
            </p>
          ) : approval.kind === "confirm" || approval.kind === "deciding" ? (
            <div className="space-y-3 rounded-lg border border-border bg-card p-3" data-testid="pair-confirm">
              <div className="flex items-start gap-3">
                <Monitor className="mt-0.5 h-4 w-4 text-muted-foreground" aria-hidden="true" />
                <div className="min-w-0">
                  <p className="text-sm font-medium text-foreground">
                    Approve machine {approval.machine.hostname || "with this code"}?
                  </p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {[approval.machine.platform, `code ${approval.machine.userCode}`].filter(Boolean).join(" · ")}. It
                    will run your projects as you. Only approve a machine you just started.
                  </p>
                </div>
              </div>
              <AuthorizeOrgPicker
                orgs={authorizeOrg.orgs}
                error={authorizeOrg.failed}
                onRetry={authorizeOrg.retry}
                orgId={authorizeOrg.orgId}
                onChoose={authorizeOrg.choose}
                disabled={approval.kind === "deciding"}
                noun="this machine"
              />
              <div className="flex gap-2">
                <Button
                  size="sm"
                  onClick={() => void decide(approval.machine, true)}
                  loading={approval.kind === "deciding" && approval.approved}
                  disabled={approval.kind === "deciding" || !authorizeOrg.ready}
                >
                  Approve
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => void decide(approval.machine, false)}
                  disabled={approval.kind === "deciding"}
                >
                  Not my machine
                </Button>
              </div>
            </div>
          ) : approval.kind === "approved" ? (
            <div className="flex items-center gap-2 text-xs text-muted-foreground" role="status">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              <span>
                Approved. Waiting for {approval.machine.hostname || "the machine"} to connect…
              </span>
            </div>
          ) : (
            <form
              className="flex gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                if (normalized.length === 8) void lookUp();
              }}
            >
              <input
                aria-label="Pairing code"
                value={code}
                onChange={(e) => setCode(formatPairingCode(e.target.value))}
                placeholder="BDFG-HJKL"
                autoComplete="off"
                spellCheck={false}
                maxLength={9}
                className="w-36 rounded-md border border-border bg-background px-3 py-1.5 font-mono text-sm uppercase tracking-widest text-foreground"
              />
              <Button type="submit" size="sm" disabled={normalized.length !== 8} loading={approval.kind === "looking"}>
                Continue
              </Button>
            </form>
          )}
          {approval.kind === "declined" && (
            <p className="text-xs text-muted-foreground">Declined. That machine was not connected.</p>
          )}
        </div>
      </section>

      {error && <p className="text-center text-xs text-destructive-ink">{error}</p>}

      {mode !== "reference" && approval.kind !== "approved" && (
        <p className="flex items-center gap-2 text-xs text-muted-foreground">
          <Download className="h-3.5 w-3.5" aria-hidden="true" />
          This screen moves on by itself once the machine connects.
        </p>
      )}
    </div>
  );
}
