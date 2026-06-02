import { useEffect, useMemo, useRef, useState } from "react";
import { create } from "@bufbuild/protobuf";
import {
  Check,
  Cloud,
  Copy,
  Download,
  Loader2,
  Monitor,
  Terminal,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { getIsDev } from "@/lib/constants";
import { grpcClient } from "@/api/grpc-client";
import { CreateDaemonTokenRequestSchema } from "@/gen/reliant/v1/daemon_token_pb";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useEventBus } from "@/lib/event-context";
import {
  useCloudEligibility,
  useCreateDaemon,
} from "@/hooks/useOnboardingQueries";
import type {
  CodeSource,
  ComputeChoice,
  OnboardingIntent,
  StepProps,
} from "../types";
import { DaemonConnectionDiagrams } from "../DaemonConnectionDiagrams";
import { daemonStartCommand, HOMEBREW_CASK_INSTALL } from "@/lib/cli-commands";
import { trackEvent } from "@/lib/analytics";

const DOWNLOAD_BASE =
  import.meta.env.VITE_DOWNLOAD_BASE_URL || "https://downloads.reliantlabs.io";
import { capabilities } from "@/services/controlPlane/capabilities";
const HAS_CLOUD_DAEMONS = capabilities.cloudDaemons;
const DAEMON_TYPE_MANAGED = 1;
const DAEMON_SIZE_SMALL = 1;

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

function codeSourceForCompute(
  current: CodeSource | undefined,
  compute: ComputeChoice,
  intent: OnboardingIntent | undefined,
): CodeSource {
  if (current === "local_folder" && compute === "cloud_free_trial")
    return "github_repo";
  if (current === "github_repo" && compute === "local_daemon")
    return "local_folder";
  if (current) return current;

  // Default codeSource when not yet set (e.g. compute pre-selected before GoalStep)
  if (intent === "existing_codebase") {
    return compute === "cloud_free_trial" ? "github_repo" : "local_folder";
  }
  if (intent === "explore") return "sample_project";
  return "new_project";
}

export function ComputeStep({
  plan,
  updatePlan,
  onNext,
  hideHeader,
}: StepProps & { hideHeader?: boolean }) {
  const [showLocal, setShowLocal] = useState(plan.compute === "local_daemon");
  const [showOtherPlatforms, setShowOtherPlatforms] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pat, setPat] = useState<string | null>(null);
  const [generatingPat, setGeneratingPat] = useState(false);
  const [patCopied, setPatCopied] = useState(false);
  const [manualFeedback, setManualFeedback] = useState<string | null>(null);
  const { activeDaemon, daemons, loading: daemonLoading } = useDaemonStatus();
  const events = useEventBus();
  const hasAdvanced = useRef(false);
  const hasTrackedConnectedRef = useRef(false);

  // ──────────────────────────────────────────────────────────────────────────
  // CLI install state (Electron only). When the user runs the desktop app,
  // electron's main process auto-installs `reliant` to PATH at first launch
  // (see electron/src/cli-installer.js). We poll the install status so the
  // onboarding step can suppress the "Run this terminal command" instructions
  // when the CLI is already available, and offer a one-click install when
  // it is not.
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

  const handleInstallCli = async () => {
    if (!window.electronAPI?.installCLI) return;
    setInstallingCli(true);
    setError(null);
    try {
      const result = await window.electronAPI.installCLI();
      if (result.success) {
        // Re-check status to update UI.
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

  // Cloud eligibility via React Query
  const isDev = getIsDev();
  const {
    eligible: cloudEligible,
    reason: cloudReason,
    isLoading: cloudLoading,
  } = useCloudEligibility();
  const eligible = isDev ? true : cloudEligible;
  const reason = isDev ? null : cloudReason;
  const loading = isDev ? false : cloudLoading;

  // Daemon mutations via React Query
  const createDaemonMutation = useCreateDaemon();

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

  // `isStartingCloud` covers the gap between click and the first mutation
  // firing — dynamic chunk imports + the listDaemons() round-trip can take
  // 100ms–2s on a cold load, during which the button would otherwise look
  // idle and invite a second click.
  const [isStartingCloud, setIsStartingCloud] = useState(false);
  const startingCloud = isStartingCloud || createDaemonMutation.isPending;

  const handleCloud = async () => {
    if (!HAS_CLOUD_DAEMONS) return;
    if (isStartingCloud) return;

    setError(null);
    setShowLocal(false);
    setIsStartingCloud(true);
    try {
      // Resolve the right path for this user. If a daemon already exists
      // (e.g. from a prior session) we either reuse it or resume it — the
      // server-side CreateDaemon is NOT a wake-up for a suspended workspace,
      // so calling it again leaves a suspended daemon suspended.
      const { listDaemons, hasActiveDaemon, resumeDaemon } =
        await import("@/services/controlPlane/daemon");
      const existing = await listDaemons();
      const daemons = existing.daemons;

      let needsProvisioning = true;

      if (hasActiveDaemon(daemons)) {
        // Active daemon already present — nothing to do.
        needsProvisioning = false;
      } else if (daemons.length > 0) {
        // Daemon exists but isn't active (suspended / disconnected / failed).
        // Resume it. Resume failure is non-fatal — the user can retry from
        // the chat banner; we still proceed to the provisioning UI so the
        // app waits for state to settle.
        const daemonId = daemons[0]?.id ?? "";
        if (daemonId) {
          try {
            await resumeDaemon(daemonId);
          } catch {
            // Surface as a soft error via the provisioning UI rather than
            // blocking onboarding.
          }
        }
      } else {
        // No daemons → create one. createDaemon may itself 409 if another
        // tab raced us; treat "already exists" as a resume.
        try {
          await createDaemonMutation.mutateAsync({
            name: "onboarding-daemon",
            daemonType: DAEMON_TYPE_MANAGED,
            size: DAEMON_SIZE_SMALL,
            gitRepo: "",
            gitBranch: "main",
          });
        } catch (err) {
          const msg = err instanceof Error ? err.message.toLowerCase() : "";
          if (
            msg.includes("plan limit") ||
            msg.includes("already") ||
            msg.includes("exists")
          ) {
            const fallback = await listDaemons();
            const fallbackId = fallback.daemons[0]?.id ?? "";
            if (fallbackId) {
              try {
                await resumeDaemon(fallbackId);
              } catch {
                /* non-fatal */
              }
            }
          } else {
            throw err;
          }
        }
      }

      await updatePlan({
        compute: "cloud_free_trial",
        daemonLocation: "reliant_cloud",
        daemonProvisioning: needsProvisioning,
        codeSource: codeSourceForCompute(
          plan.codeSource,
          "cloud_free_trial",
          plan.intent,
        ),
        localPath: undefined,
        projectName: undefined,
      });
      trackEvent("onboarding_compute_selected", { compute: "cloud" });
      onNext();
    } catch (err) {
      const msg =
        err instanceof Error ? err.message : "Failed to start hosted daemon";
      setError(msg);
      events.emit("toast:show", { message: msg, variant: "error" });
    } finally {
      setIsStartingCloud(false);
    }
  };

  // Clicking "I'll connect my own" only flips local UI state — it does NOT
  // commit `plan.compute` yet. If we set it here, `deriveStep` would see
  // compute set + modelProvider unset and immediately route the user to the
  // model step, skipping the download/connect instructions that render below
  // when `showLocal && !activeDaemon`. We commit compute once the daemon
  // actually connects (the useEffect below) or via the explicit Continue
  // button when a daemon is already running.
  const handleLocal = () => {
    setError(null);
    setShowLocal(true);
  };

  const commitLocalAndAdvance = async (daemonPreconnected: boolean) => {
    if (hasAdvanced.current) return;
    hasAdvanced.current = true;
    await updatePlan({
      compute: "local_daemon",
      daemonLocation: "self_hosted",
      daemonProvisioning: false,
      codeSource: codeSourceForCompute(
        plan.codeSource,
        "local_daemon",
        plan.intent,
      ),
      localPath: undefined,
      projectName: undefined,
    });
    trackEvent("onboarding_compute_selected", {
      compute: "local",
      daemon_preconnected: daemonPreconnected,
    });
    onNext();
  };

  const handleLocalContinue = async () => {
    await commitLocalAndAdvance(Boolean(activeDaemon));
  };

  // Auto-skip the compute step whenever a daemon is already active. Two
  // cases this covers:
  //   1. Initial mount with a daemon already running (Electron's main
  //      process auto-starts the daemon on first launch; users on the web
  //      may also have one from a prior session).
  //   2. User clicked "I'll connect my own", followed the instructions,
  //      and their newly-started daemon just connected.
  // The `!startingCloud` guard avoids racing handleCloud: while the cloud
  // flow is running, an already-active daemon (e.g. a stale local one)
  // must not pre-empt the cloud commit.
  useEffect(() => {
    if (!activeDaemon) return;
    if (startingCloud) return;
    if (hasAdvanced.current) return;
    if (!hasTrackedConnectedRef.current) {
      hasTrackedConnectedRef.current = true;
      trackEvent("onboarding_daemon_connected");
    }
    void commitLocalAndAdvance(true);
    // commitLocalAndAdvance closes over plan.codeSource / plan.intent /
    // updatePlan / onNext, but the hasAdvanced ref guards against re-entry,
    // so we intentionally narrow the dep list to the trigger conditions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeDaemon, startingCloud]);

  const handleGeneratePat = async () => {
    setGeneratingPat(true);
    setError(null);
    try {
      const hostname =
        typeof window !== "undefined" && window.location
          ? `onboarding-${window.location.hostname}`
          : "onboarding";
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

  const handleManualCheck = () => {
    if (activeDaemon) {
      setManualFeedback("Daemon connected. Moving on...");
      handleLocalContinue();
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

  const handleCopyPat = async () => {
    if (!pat) return;
    await navigator.clipboard.writeText(pat);
    setPatCopied(true);
    setTimeout(() => setPatCopied(false), 2000);
  };

  return (
    <div className="space-y-6">
      {!showLocal && <DaemonConnectionDiagrams />}

      <div className="grid gap-3 sm:grid-cols-2">
        <div
          className={cn(
            "flex min-w-0 flex-col gap-4 rounded-xl border-2 p-5 text-left transition-all",
            plan.compute === "cloud_free_trial"
              ? "border-primary bg-primary/10"
              : "border-primary/25 bg-primary/5",
            !HAS_CLOUD_DAEMONS && "border-border/50 bg-muted/30 opacity-80",
          )}
        >
          <div className="flex min-w-0 items-start gap-4">
            <div className="flex-shrink-0 rounded-lg bg-primary/15 p-2.5 text-primary">
              {startingCloud ? (
                <Loader2 className="h-6 w-6 animate-spin" />
              ) : (
                <Cloud className="h-6 w-6" />
              )}
            </div>
            <div className="min-w-0 space-y-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-semibold text-foreground">
                  Reliant Cloud
                </span>
                <span className="rounded bg-primary/20 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider text-primary">
                  Fastest
                </span>
              </div>
              <span className="block text-xs leading-relaxed text-muted-foreground">
                {HAS_CLOUD_DAEMONS
                  ? "Start a hosted daemon now. If provisioning takes a few minutes, Reliant will continue setup and connect when it is ready."
                  : "Cloud daemons are not enabled for this environment."}
              </span>
            </div>
          </div>

          <button
            type="button"
            onClick={handleCloud}
            disabled={
              startingCloud || !HAS_CLOUD_DAEMONS || !eligible || loading
            }
            className={cn(
              "inline-flex w-full items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-semibold transition-colors",
              startingCloud || !HAS_CLOUD_DAEMONS || !eligible || loading
                ? "cursor-not-allowed bg-muted text-muted-foreground"
                : "bg-sky-600 text-white shadow-sm shadow-sky-600/20 hover:bg-sky-500",
            )}
          >
            {(startingCloud || loading) && (
              <Loader2 className="h-4 w-4 animate-spin" />
            )}
            {startingCloud
              ? "Requesting daemon..."
              : loading
                ? "Checking availability..."
                : "Start cloud daemon"}
          </button>
          {(!HAS_CLOUD_DAEMONS || (!eligible && reason)) && (
            <div className="space-y-1.5">
              <p className="text-xs leading-relaxed text-muted-foreground">
                {!HAS_CLOUD_DAEMONS
                  ? 'Cloud daemons are unavailable because this environment is not configured for cloud mode. Choose "I\'ll connect my own" to continue.'
                  : reason}
              </p>
              {HAS_CLOUD_DAEMONS && !eligible && (
                <a
                  href="https://reliantlabs.io/pricing"
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 text-xs font-medium text-sky-500 transition-colors hover:text-sky-400"
                >
                  View plans &rarr;
                </a>
              )}
            </div>
          )}
        </div>

        <button
          type="button"
          onClick={handleLocal}
          className={cn(
            "flex min-w-0 items-start gap-4 rounded-xl border-2 p-5 text-left transition-all",
            "hover:border-primary/50 hover:bg-muted/50",
            showLocal
              ? "border-primary bg-primary/10"
              : "border-border/50 bg-background",
          )}
        >
          <div className="flex-shrink-0 rounded-lg bg-muted p-2.5 text-muted-foreground">
            <Monitor className="h-6 w-6" />
          </div>
          <div className="min-w-0 space-y-1">
            <span className="block text-sm font-semibold text-foreground">
              I'll connect my own
            </span>
            <span className="block text-xs leading-relaxed text-muted-foreground">
              Run the daemon on a laptop or server that can access the directory
              you choose.
            </span>
          </div>
        </button>
      </div>

      {error && <p className="text-center text-xs text-destructive">{error}</p>}

      {showLocal && activeDaemon && (
        <div className="space-y-3 rounded-xl border border-emerald-500/30 bg-emerald-500/5 p-4">
          <div className="flex items-start gap-3">
            <Check className="mt-0.5 h-4 w-4 text-emerald-500" />
            <div>
              <h3 className="text-sm font-medium text-foreground">
                Daemon already connected
              </h3>
              <p className="mt-0.5 text-xs text-muted-foreground">
                Reliant detected a running daemon. Continue to choose a
                directory.
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={handleLocalContinue}
            className="w-full rounded-lg bg-zinc-950 py-2.5 text-sm font-medium text-white transition-colors hover:bg-zinc-800 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200"
          >
            Continue
          </button>
        </div>
      )}

      {showLocal && !activeDaemon && (
        <div className="space-y-4 rounded-xl border border-border/50 bg-muted/30 p-4">
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
              <code className="font-mono">reliant</code> CLI on your PATH and
              starts the daemon automatically — no terminal commands needed.
              This screen will move on the moment it connects.
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
              Inside the Electron app we know whether the `reliant` CLI is
              already on $PATH (the main process installs it on first launch).
              If it's missing, surface a one-click install button so the user
              doesn't have to hunt through Settings → About. When CLI install
              succeeds (or already done), we still show the start command —
              the user has to run it themselves because we don't have a token
              yet (it's in the textbox above, not in the daemon's creds file).
            */}
            {isElectron && cliInstalled === false && (
              <div className="rounded border border-amber-500/30 bg-amber-500/5 p-2.5 space-y-2">
                <p className="flex items-start gap-2 text-[11px] text-amber-700 dark:text-amber-300">
                  <Terminal className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" />
                  <span>
                    The <code className="font-mono">reliant</code> command is
                    not on your PATH yet. Install it to run the daemon from your
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
                  If you skip the CLI install, install it separately via
                  Homebrew or download the binary.
                </>
              )}
            </p>
          </div>

          <div className="space-y-2 border-t border-border/30 pt-3">
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              <span>
                Waiting for the daemon to connect. This screen will continue
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
      )}
    </div>
  );
}
