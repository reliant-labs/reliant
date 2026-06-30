import { useEffect, useMemo, useRef, useState } from "react";
import { create } from "@bufbuild/protobuf";
import {
  Check,
  Copy,
  Download,
  Loader2,
  Terminal,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { grpcClient } from "@/api/grpc-client";
import { CreateDaemonTokenRequestSchema } from "@/gen/reliant/v1/daemon_token_pb";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useEventBus } from "@/lib/event-context";
import { daemonStartCommand, HOMEBREW_CASK_INSTALL } from "@/lib/cli-commands";

const DOWNLOAD_BASE =
  import.meta.env.VITE_DOWNLOAD_BASE_URL || "https://downloads.reliantlabs.io";

type DetectedOS = "mac-arm64" | "mac-x64" | "windows" | "linux" | "unknown";

interface DownloadLink {
  label: string;
  url: string;
  os: DetectedOS;
}

const DOWNLOAD_LINKS: DownloadLink[] = [
  {
    label: "Mac (Apple Silicon)",
    url: `${DOWNLOAD_BASE}/Reliant-latest-mac-arm64.dmg`,
    os: "mac-arm64",
  },
  {
    label: "Mac (Intel)",
    url: `${DOWNLOAD_BASE}/Reliant-latest-mac-x64.dmg`,
    os: "mac-x64",
  },
  {
    label: "Windows x64",
    url: `${DOWNLOAD_BASE}/Reliant-latest-win-x64.exe`,
    os: "windows",
  },
  {
    label: "Windows ARM64",
    url: `${DOWNLOAD_BASE}/Reliant-latest-win-arm64.exe`,
    os: "windows",
  },
  {
    label: "Linux x86_64",
    url: `${DOWNLOAD_BASE}/Reliant-latest-linux-x86_64.AppImage`,
    os: "linux",
  },
  {
    label: "Linux ARM64",
    url: `${DOWNLOAD_BASE}/Reliant-latest-linux-arm64.AppImage`,
    os: "linux",
  },
];

// Synchronous best-guess based on `navigator.platform`. For Macs we default to
// arm64 (the overwhelming majority of Macs sold since 2020) and refine async
// via `detectMacArch` below. We can't read CPU arch synchronously: Chromium's
// `userAgentData.architecture` is a high-entropy hint that requires the async
// `getHighEntropyValues` call, Safari has no `userAgentData` at all, and
// `navigator.platform` reports "MacIntel" on Apple Silicon for web-compat.
function getInitialOS(): DetectedOS {
  const platform = navigator.platform;
  if (/Mac/i.test(platform)) return "mac-arm64";
  if (/Win/i.test(platform)) return "windows";
  if (/Linux/i.test(platform)) return "linux";
  return "unknown";
}

type UserAgentDataLike = {
  getHighEntropyValues?: (
    hints: string[],
  ) => Promise<{ architecture?: string }>;
};

async function detectMacArch(): Promise<"mac-arm64" | "mac-x64"> {
  const uaData = (
    navigator as Navigator & { userAgentData?: UserAgentDataLike }
  ).userAgentData;
  if (uaData?.getHighEntropyValues) {
    try {
      const { architecture } = await uaData.getHighEntropyValues([
        "architecture",
      ]);
      if (architecture === "arm") return "mac-arm64";
      if (architecture === "x86") return "mac-x64";
    } catch {
      // fall through to WebGL probe
    }
  }
  // Safari / fallback: the unmasked WebGL renderer is "Apple GPU" / "Apple M…"
  // on Apple Silicon and includes "Intel" on Intel Macs.
  try {
    const canvas = document.createElement("canvas");
    const gl =
      (canvas.getContext("webgl") as WebGLRenderingContext | null) ??
      (canvas.getContext("experimental-webgl") as WebGLRenderingContext | null);
    const ext = gl?.getExtension("WEBGL_debug_renderer_info");
    if (gl && ext) {
      const renderer = gl.getParameter(ext.UNMASKED_RENDERER_WEBGL) as string;
      if (/Intel/i.test(renderer)) return "mac-x64";
      if (/Apple/i.test(renderer)) return "mac-arm64";
    }
  } catch {
    // ignore — fall through to default
  }
  // Modern default. Intel Mac users can still click "Other platforms".
  return "mac-arm64";
}

function getPrimaryDownload(os: DetectedOS): DownloadLink | null {
  if (os === "unknown") return null;
  return DOWNLOAD_LINKS.find((link) => link.os === os) ?? null;
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
}

/**
 * SelfHostedDaemonConnect — the "I'll connect my own (self-hosted) daemon"
 * instructions. Generates a daemon token via CreateDaemonToken and walks the
 * user through download + install + `reliant daemon start --token`, then
 * waits for the daemon to connect.
 *
 * Extracted from the onboarding ComputeStep so the ProjectPicker's
 * "Connect a new daemon" flow can offer the exact same self-hosted path
 * in-place, without bouncing the user into the onboarding wizard.
 */
export function SelfHostedDaemonConnect({
  onConnected,
}: SelfHostedDaemonConnectProps) {
  const [error, setError] = useState<string | null>(null);
  const [pat, setPat] = useState<string | null>(null);
  const [generatingPat, setGeneratingPat] = useState(false);
  const [patCopied, setPatCopied] = useState(false);
  const [showOtherPlatforms, setShowOtherPlatforms] = useState(false);
  const [manualFeedback, setManualFeedback] = useState<string | null>(null);
  const { activeDaemon, daemons, loading: daemonLoading } = useDaemonStatus();
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

  const [detectedOS, setDetectedOS] = useState<DetectedOS>(() =>
    getInitialOS(),
  );
  useEffect(() => {
    if (detectedOS !== "mac-arm64") return;
    let cancelled = false;
    void detectMacArch().then((arch) => {
      if (!cancelled) setDetectedOS(arch);
    });
    return () => {
      cancelled = true;
    };
    // Only run once at mount — re-running on every detectedOS change would
    // loop after `setDetectedOS("mac-x64")` flips us off the arm64 branch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const primaryDownload = useMemo(
    () => getPrimaryDownload(detectedOS),
    [detectedOS],
  );
  const otherDownloads = useMemo(
    () => DOWNLOAD_LINKS.filter((link) => link !== primaryDownload),
    [primaryDownload],
  );

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

  if (activeDaemon) {
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

      <div className="rounded-lg border border-sky-500/30 bg-sky-500/5 p-3 text-xs leading-relaxed text-foreground">
        <span className="font-medium">Already downloaded Reliant?</span>{" "}
        <span className="text-muted-foreground">
          Opening the desktop app installs the{" "}
          <code className="font-mono">reliant</code> CLI on your PATH and starts
          the daemon automatically — no terminal commands needed. This screen
          will react the moment it connects.
        </span>
      </div>

      {primaryDownload ? (
        <div className="space-y-3">
          <a
            href={primaryDownload.url}
            className="flex w-full items-center justify-center gap-2 rounded-lg bg-sky-600 py-3 text-sm font-semibold text-white shadow-sm shadow-sky-600/20 transition-colors hover:bg-sky-500"
          >
            Download for {primaryDownload.label}
          </a>

          <button
            type="button"
            onClick={() => setShowOtherPlatforms(!showOtherPlatforms)}
            className="w-full text-center text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            {showOtherPlatforms ? "Hide" : "Other platforms"}
          </button>

          {showOtherPlatforms && (
            <div className="space-y-1.5">
              {otherDownloads.map((link) => (
                <a
                  key={link.url}
                  href={link.url}
                  className="flex items-center justify-between rounded px-3 py-2 text-xs text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
                >
                  <span>{link.label}</span>
                  <span className="text-sky-500">Download</span>
                </a>
              ))}
            </div>
          )}
        </div>
      ) : (
        <div className="space-y-1.5">
          {DOWNLOAD_LINKS.map((link) => (
            <a
              key={link.url}
              href={link.url}
              className="flex items-center justify-between rounded border border-border/40 px-3 py-2 text-xs text-foreground transition-colors hover:bg-muted/50"
            >
              <span>{link.label}</span>
              <span className="text-sky-500">Download</span>
            </a>
          ))}
        </div>
      )}

      <div className="space-y-1.5">
        <span className="block text-xs text-muted-foreground">
          Or install via Homebrew:
        </span>
        <code className="block select-all rounded border border-border/40 bg-background px-3 py-2 font-mono text-xs text-foreground">
          {HOMEBREW_CASK_INSTALL}
        </code>
      </div>

      <div className="space-y-2 border-t border-border/30 pt-3">
        <div className="flex items-center justify-between">
          <span className="text-xs font-medium text-foreground">
            1. Generate an access token
          </span>
          {pat && (
            <span className="text-[10px] uppercase tracking-wider text-emerald-500">
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
          <p className="text-[11px] text-yellow-600 dark:text-yellow-400">
            The token is shown once. Copy it now.
          </p>
        )}
      </div>

      <div className="space-y-1.5">
        <span className="block text-xs font-medium text-foreground">
          2. Start the daemon
        </span>

        {/*
          Inside the Electron app we know whether the `reliant` CLI is already
          on $PATH (the main process installs it on first launch). If it's
          missing, surface a one-click install button so the user doesn't have
          to hunt through Settings → About.
        */}
        {isElectron && cliInstalled === false && (
          <div className="rounded border border-amber-500/30 bg-amber-500/5 p-2.5 space-y-2">
            <p className="flex items-start gap-2 text-[11px] text-amber-700 dark:text-amber-300">
              <Terminal className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" />
              <span>
                The <code className="font-mono">reliant</code> command is not on
                your PATH yet. Install it to run the daemon from your terminal.
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
              {installingCli && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
              {installingCli ? "Installing..." : "Install reliant CLI"}
            </button>
          </div>
        )}

        {isElectron && cliInstalled === true && cliPath && (
          <p className="flex items-center gap-1.5 text-[11px] text-emerald-600 dark:text-emerald-400">
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
        <p className="text-[11px] text-muted-foreground">
          The command will prompt you to paste the token.
          {isElectron && cliInstalled === false && (
            <>
              {" "}
              If you skip the CLI install, install it separately via Homebrew or
              download the binary.
            </>
          )}
        </p>
      </div>

      {error && <p className="text-center text-xs text-destructive">{error}</p>}

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
    </div>
  );
}
