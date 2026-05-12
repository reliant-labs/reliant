import { useMemo, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Check, Cloud, Copy, Download, Loader2, Monitor } from "lucide-react";
import { cn } from "@/lib/utils";
import { getIsDev } from "@/lib/constants";
import { grpcClient } from "@/api/grpc-client";
import { CreateDaemonTokenRequestSchema } from "@/gen/reliant/v1/tools_daemon_pb";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useEventBus } from "@/lib/event-context";
import {
  useCloudEligibility,
  useCreateDaemon,
  useResumeDaemon,
} from "@/hooks/useOnboardingQueries";
import { hasActiveDaemon, getFirstDaemonId } from "../api";
import type { CodeSource, ComputeChoice, OnboardingIntent, StepProps } from "../types";
import { DaemonConnectionDiagrams } from "../DaemonConnectionDiagrams";
import { daemonStartCommand, HOMEBREW_CLI_INSTALL, HOMEBREW_CASK_INSTALL } from "@/lib/cli-commands";

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

function getOS(): DetectedOS {
  const platform = navigator.platform;
  if (/Mac/i.test(platform)) {
    return (
      navigator as Navigator & { userAgentData?: { architecture?: string } }
    ).userAgentData?.architecture === "arm"
      ? "mac-arm64"
      : "mac-x64";
  }
  if (/Win/i.test(platform)) return "windows";
  if (/Linux/i.test(platform)) return "linux";
  return "unknown";
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

export function ComputeStep({ plan, updatePlan, onNext, hideHeader }: StepProps & { hideHeader?: boolean }) {
  const [showLocal, setShowLocal] = useState(
    plan.compute === "local_daemon",
  );
  const [showOtherPlatforms, setShowOtherPlatforms] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pat, setPat] = useState<string | null>(null);
  const [generatingPat, setGeneratingPat] = useState(false);
  const [patCopied, setPatCopied] = useState(false);
  const { activeDaemon } = useDaemonStatus();
  const events = useEventBus();

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
  const resumeDaemonMutation = useResumeDaemon();

  const detectedOS = useMemo(() => getOS(), []);
  const primaryDownload = useMemo(
    () => getPrimaryDownload(detectedOS),
    [detectedOS],
  );
  const otherDownloads = useMemo(
    () => DOWNLOAD_LINKS.filter((link) => link !== primaryDownload),
    [primaryDownload],
  );

  const startingCloud = createDaemonMutation.isPending || resumeDaemonMutation.isPending;

  const handleCloud = async () => {
    if (!HAS_CLOUD_DAEMONS) return;

    setError(null);
    try {
      // Fetch daemons inline — we need fresh data for the decision tree
      const { queryClient } = await import("@/lib/query-client");
      const { listDaemons } = await import("../api");
      const existing = await queryClient.fetchQuery({
        queryKey: ["onboarding", "daemons"],
        queryFn: async () => {
          const { daemons } = await listDaemons();
          return daemons;
        },
        staleTime: 0,
      });
      const daemons = existing;

      if (hasActiveDaemon(daemons)) {
        // Active daemon already exists — reuse it, skip creation
        updatePlan({
          compute: "cloud_free_trial",
          daemonLocation: "reliant_cloud",
          daemonProvisioning: false,
          codeSource: codeSourceForCompute(plan.codeSource, "cloud_free_trial", plan.intent),
          localPath: undefined,
          projectName: undefined,
        });
        onNext();
        return;
      }

      if (daemons.length > 0) {
        // Daemon exists but is not active — resume it
        const daemonId = getFirstDaemonId(daemons);
        if (!daemonId) {
          throw new Error("Found existing daemon but could not determine its ID. Please try again.");
        }
        try {
          await resumeDaemonMutation.mutateAsync(daemonId);
        } catch (resumeErr) {
          console.warn("resumeDaemon failed, proceeding with provisioning state:", resumeErr);
        }
        updatePlan({
          compute: "cloud_free_trial",
          daemonLocation: "reliant_cloud",
          daemonProvisioning: true,
          codeSource: codeSourceForCompute(plan.codeSource, "cloud_free_trial", plan.intent),
          localPath: undefined,
          projectName: undefined,
        });
        onNext();
        return;
      }

      // No daemons at all — create a new one
      try {
        await createDaemonMutation.mutateAsync({
          name: "onboarding-daemon",
          daemonType: DAEMON_TYPE_MANAGED,
          size: DAEMON_SIZE_SMALL,
          gitRepo: "",
          gitBranch: "main",
        });
      } catch (err) {
        const message = err instanceof Error ? err.message.toLowerCase() : "";
        if (message.includes("plan limit") || message.includes("already") || message.includes("exists")) {
          // Plan limit or already exists — try to find and resume existing daemon
          const { listDaemons: listDaemonsFallback } = await import("../api");
          const fallback = await listDaemonsFallback();
          const fallbackId = getFirstDaemonId(fallback.daemons);
          if (fallbackId) {
            try {
              await resumeDaemonMutation.mutateAsync(fallbackId);
            } catch (resumeErr) {
              console.warn("Fallback resumeDaemon failed, proceeding:", resumeErr);
            }
          }
        } else {
          throw err;
        }
      }

      updatePlan({
        compute: "cloud_free_trial",
        daemonLocation: "reliant_cloud",
        daemonProvisioning: true,
        codeSource: codeSourceForCompute(plan.codeSource, "cloud_free_trial", plan.intent),
        localPath: undefined,
        projectName: undefined,
      });
      onNext();
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to start hosted daemon";
      setError(msg);
      events.emit("toast:show", { message: msg, variant: "error" });
    }
  };

  const handleLocal = () => {
    setError(null);
    updatePlan({
      compute: "local_daemon",
      daemonLocation: "self_hosted",
      daemonProvisioning: false,
      daemonPreConnected: Boolean(activeDaemon),
      codeSource: codeSourceForCompute(plan.codeSource, "local_daemon", plan.intent),
      localPath: undefined,
      projectName: undefined,
    });
    setShowLocal(true);
  };

  const handleLocalContinue = () => {
    handleLocal();
    onNext();
  };

  const handleGeneratePat = async () => {
    setGeneratingPat(true);
    setError(null);
    try {
      const hostname =
        typeof window !== "undefined" && window.location
          ? `onboarding-${window.location.hostname}`
          : "onboarding";
      const res = await grpcClient
        .daemonRegistry()
        .createDaemonToken(
          create(CreateDaemonTokenRequestSchema, { name: hostname }),
        );
      setPat(res.token);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to generate access token";
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

  return (
    <div className="space-y-6">
      {!hideHeader && (
        <div className="space-y-2 text-center">
          <h2 className="text-2xl font-semibold tracking-tight text-foreground">
            Where should Reliant run your daemon?
          </h2>
          <p className="text-sm text-muted-foreground">
            The daemon runs next to your code so agents can read files, run
            commands, and keep work moving.
          </p>
        </div>
      )}

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
            disabled={startingCloud || !HAS_CLOUD_DAEMONS || !eligible || loading}
            className={cn(
              "inline-flex w-full items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-semibold transition-colors",
              startingCloud || !HAS_CLOUD_DAEMONS || !eligible || loading
                ? "cursor-not-allowed bg-muted text-muted-foreground"
                : "bg-sky-600 text-white shadow-sm shadow-sky-600/20 hover:bg-sky-500",
            )}
          >
            {(startingCloud || loading) && <Loader2 className="h-4 w-4 animate-spin" />}
            {startingCloud ? "Requesting daemon..." : loading ? "Checking availability..." : "Start cloud daemon"}
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
            plan.compute === "local_daemon"
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
              Or install the CLI via Homebrew:
            </span>
            <code className="block select-all rounded border border-border/40 bg-background px-3 py-2 font-mono text-xs text-foreground">
              {HOMEBREW_CLI_INSTALL}
            </code>
            <span className="block text-xs text-muted-foreground">
              Desktop app (Homebrew Cask):
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
                {generatingPat && (
                  <Loader2 className="h-4 w-4 animate-spin" />
                )}
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
            <code className="block select-all rounded border border-border/40 bg-background px-3 py-2 font-mono text-xs text-foreground break-all">
              {daemonStartCommand()}
            </code>
            <p className="text-[11px] text-muted-foreground">
              The command will prompt you to paste the token.
            </p>
          </div>

          <button
            type="button"
            onClick={handleLocalContinue}
            className="w-full rounded-lg bg-zinc-950 py-2.5 text-sm font-medium text-white transition-colors hover:bg-zinc-800 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200"
          >
            Continue to daemon check
          </button>
        </div>
      )}
    </div>
  );
}