import { useEffect, useRef, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Check, Copy, Download, Loader2, Terminal } from "lucide-react";
import { cn } from "@/lib/utils";
import { grpcClient } from "@/api/grpc-client";
import { CreateDaemonTokenRequestSchema } from "@/gen/reliant/v1/daemon_token_pb";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useEventBus } from "@/lib/event-context";
import {
  daemonStartCommand,
  daemonStartCommandNeedsEditing,
  GATEWAY_URL_PLACEHOLDER,
} from "@/lib/cli-commands";
import {
  describeTerminal,
  ReliantDownloadOptions,
  supportsHomebrewCask,
  useDetectedOS,
} from "@/components/ReliantDownloadOptions";

/**
 * Why the caller is showing these instructions, which decides how the panel
 * behaves once a daemon is already connected:
 *
 * - "bootstrap" — the caller is blocked until SOME daemon exists (onboarding's
 *   ComputeStep, the ProjectPicker's connect modal). A connected daemon means
 *   the job is done, so the panel collapses to a success card and reports up
 *   via `onConnected` so the caller can advance or dismiss.
 * - "reference" — the caller is standing documentation for adding ANOTHER
 *   machine (Settings → Machines). An existing connection says nothing about
 *   whether the user still wants to set up the machine in front of them, so
 *   the instructions stay put and the flow-control affordances (waiting
 *   spinner, "check connection" button) are dropped.
 */
export type SelfHostedDaemonConnectMode = "bootstrap" | "reference";

/**
 * A numbered step label. The steps were previously "1." and "2." rendered as
 * bare text with downloading numbered as nothing at all, so the sequence
 * started at the second thing the user had to do.
 */
function StepHeading({
  index,
  title,
  tone = "neutral",
}: {
  index: number;
  title: string;
  tone?: "sky" | "neutral";
}) {
  return (
    <div className="flex items-center gap-2">
      <span
        className={cn(
          "flex h-5 w-5 flex-shrink-0 items-center justify-center rounded-full text-2xs font-semibold",
          tone === "sky"
            ? "bg-sky-500/20 text-sky-700 dark:text-sky-300"
            : "bg-muted text-muted-foreground",
        )}
      >
        {index}
      </span>
      <span className="text-xs font-medium text-foreground">{title}</span>
    </div>
  );
}

interface SelfHostedDaemonConnectProps {
  /**
   * Fired once, the first time a daemon connects (status flips to ACTIVE)
   * while this panel is mounted. Callers use it to advance their own flow
   * (e.g. onboarding's ComputeStep auto-advance, or the picker dismissing
   * its connect modal). Optional — when omitted the panel just shows the
   * waiting state and lets the surrounding UI react to the daemon list.
   */
  onConnected?: () => void;
  /** See SelfHostedDaemonConnectMode. Defaults to "bootstrap". */
  mode?: SelfHostedDaemonConnectMode;
}

/**
 * SelfHostedDaemonConnect — the "I'll connect my own (self-hosted) daemon"
 * instructions. Generates a daemon token via CreateDaemonToken and walks the
 * user through download + install + `reliant daemon start --token`, then
 * waits for the daemon to connect.
 *
 * This is the single source of truth for "how do I download and set up
 * Reliant on my own machine." Onboarding's ComputeStep, the ProjectPicker's
 * "Connect a new daemon" modal, and Settings → Machines all render this same
 * panel, so the download links, the platform-gated Homebrew cask, the token
 * step, and the start command only ever have to be right in one place.
 */
export function SelfHostedDaemonConnect({
  onConnected,
  mode = "bootstrap",
}: SelfHostedDaemonConnectProps) {
  const [error, setError] = useState<string | null>(null);
  const [pat, setPat] = useState<string | null>(null);
  const [generatingPat, setGeneratingPat] = useState(false);
  const [patCopied, setPatCopied] = useState(false);
  const [manualFeedback, setManualFeedback] = useState<string | null>(null);
  const { activeDaemon, daemons, loading: daemonLoading } = useDaemonStatus();
  const detectedOS = useDetectedOS();
  const events = useEventBus();
  const notifiedConnectedRef = useRef(false);

  // ──────────────────────────────────────────────────────────────────────────
  // CLI install state (Electron only). When the user runs the desktop app,
  // electron's main process auto-installs `reliant` to PATH at first launch
  // (see electron/src/cli-installer.js). We poll the install status so we can
  // suppress the "Run this terminal command" instructions when the CLI is
  // already available, and offer a one-click install when it is not.
  // ──────────────────────────────────────────────────────────────────────────
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
        if (cancelled) return;
        setCliInstalled(false);
      });
    return () => {
      cancelled = true;
    };
  }, [isElectron]);

  // Notify the caller exactly once when a daemon connects.
  useEffect(() => {
    if (daemonLoading) return;
    if (!activeDaemon) return;
    if (notifiedConnectedRef.current) return;
    notifiedConnectedRef.current = true;
    onConnected?.();
  }, [activeDaemon, daemonLoading, onConnected]);

  const handleInstallCli = async () => {
    if (!window.electronAPI?.installCLI) return;
    setInstallingCli(true);
    setError(null);
    try {
      const result = await window.electronAPI.installCLI();
      if (result.success) {
        if (window.electronAPI.getCliStatus) {
          const status = await window.electronAPI.getCliStatus();
          setCliInstalled(status.installed);
          setCliPath(status.path);
        } else {
          setCliInstalled(true);
        }
        events.emit("toast:show", {
          message: result.message || "CLI installed",
          variant: "success",
        });
      } else {
        const msg = result.error || "Failed to install CLI";
        setError(msg);
        events.emit("toast:show", { message: msg, variant: "error" });
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to install CLI";
      setError(msg);
      events.emit("toast:show", { message: msg, variant: "error" });
    } finally {
      setInstallingCli(false);
    }
  };

  const handleGeneratePat = async () => {
    setGeneratingPat(true);
    setError(null);
    try {
      const hostname =
        typeof window !== "undefined" && window.location
          ? `connect-${window.location.hostname}`
          : "connect";
      const res = await grpcClient
        .daemonToken()
        .createDaemonToken(
          create(CreateDaemonTokenRequestSchema, { name: hostname }),
        );
      setPat(res.token);
    } catch (err) {
      const msg =
        err instanceof Error ? err.message : "Failed to generate access token";
      setError(msg);
      events.emit("toast:show", { message: msg, variant: "error" });
    } finally {
      setGeneratingPat(false);
    }
  };

  const handleCopyPat = async () => {
    if (!pat) return;
    await navigator.clipboard.writeText(pat);
    setPatCopied(true);
    setTimeout(() => setPatCopied(false), 2000);
  };

  // ──────────────────────────────────────────────────────────────────────────
  // Is Reliant already installed SOMEWHERE? — decides whether step 1 is folded
  //
  // Three independent signals, weakest last:
  //
  //   1. `cliInstalled` — the `reliant` CLI is on PATH on THIS machine. The
  //      strongest: the download would install the thing we just found.
  //   2. `isElectron` — this is the desktop app, so Reliant is by definition
  //      downloaded and running on the machine the user is reading from.
  //   3. `daemons.length > 0` — the account has a daemon registered. Weaker
  //      than the other two, because the daemon may be on a different machine
  //      entirely, which is precisely why this FOLDS the block instead of
  //      removing it.
  //
  // Each note names the evidence rather than asserting a conclusion, so a
  // user setting up a second machine can see why we folded it and reopen it.
  // ──────────────────────────────────────────────────────────────────────────
  const installLikelyDone =
    cliInstalled === true || isElectron || daemons.length > 0;
  const installEvidenceNote =
    cliInstalled === true
      ? "The reliant CLI is already installed on this machine."
      : isElectron
        ? "You are running the Reliant desktop app, so it is already installed here."
        : daemons.length > 0
          ? "You already have a machine connected. Open this if you are setting up another one."
          : undefined;

  const terminal = describeTerminal(detectedOS);

  const handleManualCheck = () => {
    if (activeDaemon) {
      setManualFeedback("Daemon connected.");
      return;
    }
    if (daemonLoading) {
      setManualFeedback(
        "Still checking daemon status. Keep this screen open and try again in a moment.",
      );
      return;
    }
    if (daemons.length > 0) {
      setManualFeedback(
        "A daemon was found, but it is not active yet. Make sure it is still running, then try again.",
      );
      return;
    }
    setManualFeedback(
      "No active daemon detected yet. Start the daemon, wait a few seconds, then check again.",
    );
  };

  if (activeDaemon && mode === "bootstrap") {
    return (
      <div className="space-y-3 rounded-xl border border-emerald-500/30 bg-emerald-500/5 p-4">
        <div className="flex items-start gap-3">
          <Check className="mt-0.5 h-4 w-4 text-emerald-500" />
          <div>
            <h3 className="text-sm font-medium text-foreground">
              Daemon connected
            </h3>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Reliant detected a running daemon. You can close this and pick a
              project.
            </p>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-3">
        <Download className="mt-0.5 h-4 w-4 text-primary" />
        <div>
          <h3 className="text-sm font-medium text-foreground">
            Install Reliant Daemon and connect with a token
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Generate an access token below, then paste it into the daemon
            command in your terminal.
          </p>
        </div>
      </div>

      {/* ── STEP 1: get Reliant onto the machine ───────────────────────────
      
          This used to be a bare <ReliantDownloadOptions /> sitting between a
          sky-tinted "already downloaded?" note and an unlabelled `border-t`
          that opened "1. Generate an access token". Three problems, all the
          same problem: the download block had no heading of its own, so it
          read as body copy belonging to the note above it; the steps were
          numbered 1 and 2 with downloading numbered as nothing, so the one
          action most users start with was outside the sequence; and a 1px
          hairline was the only thing dividing "get the app" from "now do this
          in a terminal". The reported symptom was that the download
          instructions blend into the post-download instructions.
          
          So the install is step 1 of three, in its own tinted container, and
          the two terminal steps share one neutral container beneath it. The
          tint is the separator the hairline was trying to be — sky for "get
          the software", recessed neutral for "then configure it" — and the
          numbering now covers the whole sequence, so nothing the user has to
          do is unnumbered. */}
      <section className="space-y-3 rounded-xl border border-sky-500/30 bg-sky-500/5 p-4">
        <StepHeading
          index={1}
          title="Install Reliant on that machine"
          tone="sky"
        />
        <p className="text-xs leading-relaxed text-muted-foreground">
          <span className="font-medium text-foreground">
            Already downloaded Reliant?
          </span>{" "}
          Opening the desktop app installs the{" "}
          <code className="font-mono">reliant</code> CLI on your PATH and starts
          the daemon automatically — no terminal commands needed. This screen
          will react the moment it connects.
        </p>
        {/* Folded when we have evidence Reliant is already on a machine —
            see `installLikelyDone`. Still reachable, because the evidence is
            about THIS machine and the user may be setting up another. */}
        <ReliantDownloadOptions
          defaultCollapsed={installLikelyDone}
          collapsedNote={installEvidenceNote}
        />
      </section>

      {/* ── STEPS 2–3: what to do once it is installed ─────────────────────
      
          One recessed container for both terminal steps, so the boundary
          against the tinted install block above is a surface change rather
          than a hairline. `bg-background` is the inset token that recesses in
          BOTH light and dark; `bg-muted` would lift in dark and sink in light
          (see the elevation note in web/src/components/Settings/cloud/ui/card.tsx). */}
      <section className="space-y-4 rounded-xl border border-border/60 bg-background p-4">
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <StepHeading index={2} title="Generate an access token" />
            {pat && (
              <span className="text-2xs uppercase tracking-wider text-emerald-500">
                Ready
              </span>
            )}
          </div>
          {pat ? (
            <div className="flex items-center gap-2">
              <code className="flex-1 select-all truncate rounded border border-border/40 bg-background px-3 py-2 font-mono text-xs text-foreground">
                {pat}
              </code>
              <button
                type="button"
                onClick={handleCopyPat}
                className="flex items-center gap-1.5 rounded-lg border border-border/40 bg-background px-3 py-2 text-xs font-medium text-foreground transition-colors hover:bg-muted"
              >
                {patCopied ? (
                  <>
                    <Check className="h-3.5 w-3.5 text-emerald-500" />
                    Copied
                  </>
                ) : (
                  <>
                    <Copy className="h-3.5 w-3.5" />
                    Copy
                  </>
                )}
              </button>
            </div>
          ) : (
            <button
              type="button"
              onClick={handleGeneratePat}
              disabled={generatingPat}
              className={cn(
                "inline-flex w-full items-center justify-center gap-2 rounded-lg py-2.5 text-sm font-medium transition-colors",
                generatingPat
                  ? "cursor-not-allowed bg-muted text-muted-foreground"
                  : "bg-sky-600 text-white shadow-sm shadow-sky-600/20 hover:bg-sky-500",
              )}
            >
              {generatingPat && <Loader2 className="h-4 w-4 animate-spin" />}
              {generatingPat ? "Generating..." : "Generate token"}
            </button>
          )}
          {pat && (
            <p className="text-xs text-yellow-600 dark:text-yellow-400">
              The token is shown once. Copy it now.
            </p>
          )}
        </div>

        <div className="space-y-1.5 border-t border-border/50 pt-4">
          <StepHeading
            index={3}
            title={`Start the daemon in ${terminal.name}`}
          />

          {/* Say HOW to open the terminal, not just what to type.
        
            "2. Start the daemon" above a code block assumes the reader knows
            they need a terminal, knows their OS ships one, and knows how to
            open it. That is the step where a non-developer stops, and it was
            the only step with no instruction attached — the code block was
            the whole of it.
            
            `describeTerminal` picks the app by detected OS so the name is one
            a user can actually search for in their launcher: Terminal on
            macOS and Linux, PowerShell on Windows. Wrong-OS guidance would be
            worse than none, so an unknown platform gets the generic wording
            rather than a guess. */}
          <p className="text-xs leading-relaxed text-muted-foreground">
            {terminal.howToOpen} Then paste this in and press{" "}
            <span className="font-medium text-foreground">Enter</span>:
          </p>

          {/*
          Inside the Electron app we know whether the `reliant` CLI is already
          on $PATH (the main process installs it on first launch). If it's
          missing, surface a one-click install button so the user doesn't have
          to hunt through Settings → About.
        */}
          {isElectron && cliInstalled === false && (
            <div className="rounded border border-amber-500/30 bg-amber-500/5 p-2.5 space-y-2">
              <p className="flex items-start gap-2 text-xs text-amber-700 dark:text-amber-300">
                <Terminal className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" />
                <span>
                  The <code className="font-mono">reliant</code> command is not
                  on your PATH yet. Install it to run the daemon from your
                  terminal.
                </span>
              </p>
              <button
                type="button"
                onClick={handleInstallCli}
                disabled={installingCli}
                className={cn(
                  "inline-flex w-full items-center justify-center gap-2 rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
                  installingCli
                    ? "cursor-not-allowed bg-muted text-muted-foreground"
                    : "bg-zinc-950 text-white hover:bg-zinc-800 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200",
                )}
              >
                {installingCli && (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                )}
                {installingCli ? "Installing..." : "Install reliant CLI"}
              </button>
            </div>
          )}

          {isElectron && cliInstalled === true && cliPath && (
            <p className="flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
              <Check className="h-3 w-3" />
              <span>
                <code className="font-mono">reliant</code> CLI installed at{" "}
                <code className="font-mono">{cliPath}</code>
              </span>
            </p>
          )}

          <code className="block select-all rounded border border-border/40 bg-background px-3 py-2 font-mono text-xs text-foreground break-all">
            {daemonStartCommand()}
          </code>
          {daemonStartCommandNeedsEditing() && (
            <p className="text-xs text-yellow-600 dark:text-yellow-400">
              Replace {GATEWAY_URL_PLACEHOLDER} with your daemon-gateway address
              before running this. It is a separate process from the API server,
              so the daemon cannot infer it on localhost.
            </p>
          )}
          <p className="text-xs text-muted-foreground">
            The command will prompt you to paste the token.
            {isElectron && cliInstalled === false && (
              <>
                {" "}
                If you skip the CLI install, reopening the desktop app adds{" "}
                <code className="font-mono">reliant</code> to your PATH
                {supportsHomebrewCask(detectedOS)
                  ? ", or you can install the cask with Homebrew"
                  : ""}
                .
              </>
            )}
          </p>
        </div>
      </section>

      {error && <p className="text-center text-xs text-destructive">{error}</p>}

      {/* Flow control, not instruction: only a caller that is waiting on a
          daemon wants a spinner and a "check connection" button. In reference
          mode the user may already have a working machine and is simply
          reading how to add another. */}
      {mode === "bootstrap" && (
        <div className="space-y-2 border-t border-border/30 pt-3">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            <span>
              Waiting for the daemon to connect. This screen will react
              automatically.
            </span>
          </div>
          <button
            type="button"
            onClick={handleManualCheck}
            className="w-full rounded-lg bg-zinc-950 py-2.5 text-sm font-medium text-white transition-colors hover:bg-zinc-800 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200"
          >
            I've started the daemon — check connection
          </button>
          {manualFeedback && (
            <p className="text-center text-xs text-muted-foreground">
              {manualFeedback}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
