import { useEffect, useMemo, useState } from "react";

import { cn } from "@/lib/utils";
import { HOMEBREW_CASK_INSTALL } from "@/lib/cli-commands";

/**
 * "How do I get Reliant onto this machine?" — the single answer, shared by
 * every surface that has to ask.
 *
 * This was previously inlined in SelfHostedDaemonConnect, so the OAuth helper
 * panel (which asks the very same question when `reliant auth serve` is not
 * running) could only offer the Homebrew one-liner. That is wrong on Windows
 * and Linux, where there is no Homebrew and the panel offered no alternative —
 * a dead end on the only screen that could unblock the user.
 *
 * The arch detection is worth keeping in one place too: it is a two-stage
 * guess (synchronous `navigator.platform`, then an async refinement) that is
 * easy to get subtly wrong, and duplicating it would mean two components
 * disagreeing about which build a Mac should get.
 */

const DOWNLOAD_BASE =
  import.meta.env.VITE_DOWNLOAD_BASE_URL || "https://downloads.reliantlabs.io";

export type DetectedOS = "mac-arm64" | "mac-x64" | "windows" | "linux" | "unknown";

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
  {
    label: "Linux .deb (Debian/Ubuntu, x86_64)",
    url: `${DOWNLOAD_BASE}/Reliant-latest-linux-amd64.deb`,
    os: "linux",
  },
  {
    label: "Linux .deb (Debian/Ubuntu, ARM64)",
    url: `${DOWNLOAD_BASE}/Reliant-latest-linux-arm64.deb`,
    os: "linux",
  },
];

/** Homebrew casks are macOS-only, and we publish no Linux cask. */
export function supportsHomebrewCask(os: DetectedOS): boolean {
  return os === "mac-arm64" || os === "mac-x64";
}

// Synchronous best-guess from `navigator.platform`. Macs default to arm64 (the
// overwhelming majority sold since 2020) and are refined asynchronously below.
// CPU arch cannot be read synchronously: Chromium's `userAgentData.architecture`
// is a high-entropy hint behind an async call, Safari has no `userAgentData` at
// all, and `navigator.platform` reports "MacIntel" on Apple Silicon for
// web-compat reasons.
function getInitialOS(): DetectedOS {
  const platform = navigator.platform;
  if (/Mac/i.test(platform)) return "mac-arm64";
  if (/Win/i.test(platform)) return "windows";
  if (/Linux/i.test(platform)) return "linux";
  return "unknown";
}

type UserAgentDataLike = {
  getHighEntropyValues?: (hints: string[]) => Promise<{ architecture?: string }>;
};

async function detectMacArch(): Promise<"mac-arm64" | "mac-x64"> {
  const uaData = (navigator as Navigator & { userAgentData?: UserAgentDataLike })
    .userAgentData;
  if (uaData?.getHighEntropyValues) {
    try {
      const { architecture } = await uaData.getHighEntropyValues(["architecture"]);
      if (architecture === "arm") return "mac-arm64";
      if (architecture === "x86") return "mac-x64";
    } catch {
      // fall through to the WebGL probe
    }
  }
  // Safari / fallback: the unmasked WebGL renderer reads "Apple GPU" / "Apple M…"
  // on Apple Silicon and contains "Intel" on Intel Macs.
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
    // ignore — fall through to the default
  }
  // Modern default. Intel Mac users can still reach their build via
  // "Other platforms".
  return "mac-arm64";
}

/** Detects this machine's OS/arch, refining the Mac guess asynchronously. */
export function useDetectedOS(): DetectedOS {
  const [detectedOS, setDetectedOS] = useState<DetectedOS>(getInitialOS);

  useEffect(() => {
    if (detectedOS !== "mac-arm64") return;
    let cancelled = false;
    void detectMacArch().then((arch) => {
      if (!cancelled) setDetectedOS(arch);
    });
    return () => {
      cancelled = true;
    };
    // Mount only: re-running when detectedOS changes would loop after
    // setDetectedOS("mac-x64") flips us off the arm64 branch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return detectedOS;
}

export interface ReliantDownloadOptionsProps {
  /** Compact sizing for dense cards (onboarding, modals). */
  size?: "default" | "compact";
  className?: string;
}

/**
 * Platform-detected download button, an "Other platforms" disclosure with
 * every build, and — on macOS only — the Homebrew one-liner as a secondary
 * path.
 */
export function ReliantDownloadOptions({
  size = "default",
  className,
}: ReliantDownloadOptionsProps) {
  const detectedOS = useDetectedOS();
  const [showOtherPlatforms, setShowOtherPlatforms] = useState(false);
  const compact = size === "compact";

  const primaryDownload = useMemo(
    () =>
      detectedOS === "unknown"
        ? null
        : (DOWNLOAD_LINKS.find((link) => link.os === detectedOS) ?? null),
    [detectedOS],
  );
  const otherDownloads = useMemo(
    () => DOWNLOAD_LINKS.filter((link) => link !== primaryDownload),
    [primaryDownload],
  );

  return (
    <div className={cn("space-y-3", className)}>
      {primaryDownload ? (
        <div className="space-y-3">
          <a
            href={primaryDownload.url}
            className={cn(
              "flex w-full items-center justify-center gap-2 rounded-lg bg-sky-600 font-semibold text-white shadow-sm shadow-sky-600/20 transition-colors hover:bg-sky-500",
              compact ? "py-2.5 text-xs" : "py-3 text-sm",
            )}
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
        // Unknown platform: show every build rather than guessing wrong.
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

      {/* Platform-neutral and true everywhere: the installer above is the
          whole story on Windows and Linux, where there is no package manager
          we publish to. */}
      <p className="text-xs text-muted-foreground">
        The installer ships the{" "}
        <code className="font-mono text-foreground">reliant</code> CLI and adds
        it to your PATH the first time you open the app — no separate download.
      </p>

      {/* Homebrew casks are macOS-only. Rendering this unconditionally sent
          Windows and Linux users to a command that cannot work, on the one
          screen meant to unblock them. On an unknown platform it is offered
          as a labelled alternative rather than as *the* answer. */}
      {supportsHomebrewCask(detectedOS) || detectedOS === "unknown" ? (
        <div className="space-y-1.5">
          <span className="block text-xs text-muted-foreground">
            {detectedOS === "unknown"
              ? "On macOS, you can install via Homebrew instead:"
              : "Or install via Homebrew:"}
          </span>
          <code className="block select-all rounded border border-border/40 bg-background px-3 py-2 font-mono text-xs text-foreground">
            {HOMEBREW_CASK_INSTALL}
          </code>
        </div>
      ) : null}
    </div>
  );
}
