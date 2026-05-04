import { CheckCircle2, ChevronDown, Circle, Cloud, Cpu, Download } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { cn } from '../../../../lib/utils';
import type { StepProps, ComputeChoice } from '../../types';

type DownloadTarget =
  | 'mac-arm64'
  | 'mac-x64'
  | 'windows-x64'
  | 'windows-arm64'
  | 'linux-x64'
  | 'linux-arm64';
type DetectedDownloadTarget = DownloadTarget | 'unknown';

interface BrowserUserAgentData {
  platform?: string;
  getHighEntropyValues?: (hints: string[]) => Promise<{
    architecture?: string;
    platform?: string;
  }>;
}

interface NavigatorWithUserAgentData extends Navigator {
  userAgentData?: BrowserUserAgentData;
}

interface DownloadLink {
  label: string;
  url: string;
  target: DownloadTarget;
}

const DOWNLOAD_LINKS: DownloadLink[] = [
  { label: 'Mac (Apple Silicon)', url: 'https://downloads.reliantlabs.io/Reliant-latest-mac-arm64.dmg', target: 'mac-arm64' },
  { label: 'Mac (Intel)', url: 'https://downloads.reliantlabs.io/Reliant-latest-mac-x64.dmg', target: 'mac-x64' },
  { label: 'Windows x64', url: 'https://downloads.reliantlabs.io/Reliant-latest-win-x64.exe', target: 'windows-x64' },
  { label: 'Windows ARM64', url: 'https://downloads.reliantlabs.io/Reliant-latest-win-arm64.exe', target: 'windows-arm64' },
  { label: 'Linux x86_64', url: 'https://downloads.reliantlabs.io/Reliant-latest-linux-x86_64.AppImage', target: 'linux-x64' },
  { label: 'Linux ARM64', url: 'https://downloads.reliantlabs.io/Reliant-latest-linux-arm64.AppImage', target: 'linux-arm64' },
];

function getDownloadTargetFromPlatform(
  platform: string | undefined,
  architecture?: string,
): DetectedDownloadTarget {
  const platformText = platform?.toLowerCase() ?? '';
  const architectureText = architecture?.toLowerCase() ?? '';
  const isArm = /arm|aarch64/.test(architectureText);
  const isX64 = /x86|x64|amd64|intel/.test(architectureText);

  if (/mac|darwin/.test(platformText)) {
    return isArm || !isX64 ? 'mac-arm64' : 'mac-x64';
  }
  if (/win/.test(platformText)) {
    return isArm ? 'windows-arm64' : 'windows-x64';
  }
  if (/linux/.test(platformText)) {
    return isArm ? 'linux-arm64' : 'linux-x64';
  }
  return 'unknown';
}

function getFallbackDownloadTarget(): DetectedDownloadTarget {
  if (typeof navigator === 'undefined') return 'unknown';

  const navigatorWithUAData = navigator as NavigatorWithUserAgentData;
  const platform =
    navigatorWithUAData.userAgentData?.platform ||
    navigator.platform ||
    navigator.userAgent;
  const platformAndAgent = `${platform} ${navigator.userAgent}`;
  const isMac = /mac|darwin/i.test(platformAndAgent);
  const architecture = /arm|aarch64/i.test(platformAndAgent)
    ? 'arm'
    : !isMac && /x86|x64|amd64|intel|win64|wow64/i.test(platformAndAgent)
      ? 'x86'
      : undefined;

  return getDownloadTargetFromPlatform(platform, architecture);
}

async function detectDownloadTarget(): Promise<DetectedDownloadTarget> {
  if (typeof navigator === 'undefined') return 'unknown';

  const navigatorWithUAData = navigator as NavigatorWithUserAgentData;
  const userAgentData = navigatorWithUAData.userAgentData;

  if (userAgentData?.getHighEntropyValues) {
    try {
      const highEntropy = await userAgentData.getHighEntropyValues([
        'architecture',
        'platform',
      ]);
      const target = getDownloadTargetFromPlatform(
        highEntropy.platform || userAgentData.platform || navigator.platform,
        highEntropy.architecture,
      );
      if (target !== 'unknown') return target;
    } catch {
      // Fall back to lower-entropy platform hints below.
    }
  }

  return getFallbackDownloadTarget();
}

function getPrimaryDownload(target: DetectedDownloadTarget): DownloadLink | null {
  if (target === 'unknown') return null;
  return DOWNLOAD_LINKS.find((link) => link.target === target) ?? null;
}

export function ComputeStep({ plan, updatePlan, onNext }: StepProps) {
  const [showLocal, setShowLocal] = useState(plan.compute === 'local_daemon');
  const [showOtherPlatforms, setShowOtherPlatforms] = useState(false);
  const [detectedTarget, setDetectedTarget] = useState<DetectedDownloadTarget>(
    () => getFallbackDownloadTarget(),
  );

  useEffect(() => {
    let cancelled = false;

    void detectDownloadTarget().then((target) => {
      if (!cancelled) {
        setDetectedTarget(target);
      }
    });

    return () => {
      cancelled = true;
    };
  }, []);

  const primaryDownload = useMemo(() => getPrimaryDownload(detectedTarget), [detectedTarget]);
  const otherDownloads = useMemo(
    () => DOWNLOAD_LINKS.filter((l) => l !== primaryDownload),
    [primaryDownload],
  );
  const isCloudSelected = plan.compute === 'cloud_free_trial';
  const isLocalSelected = plan.compute === 'local_daemon';

  const handleCloud = () => {
    updatePlan({ compute: 'cloud_free_trial' as ComputeChoice });
    onNext();
  };

  const handleLocal = () => {
    updatePlan({ compute: 'local_daemon' as ComputeChoice });
    setShowLocal(true);
  };

  return (
    <div className="space-y-6">
      <div className="text-center space-y-2">
        <h2 className="text-xl font-semibold text-foreground">
          Where should Reliant run?
        </h2>
        <p className="text-sm text-muted-foreground">
          Choose cloud compute to get started instantly, or run locally on your machine.
        </p>
      </div>

      <div className="flex flex-col gap-3">
        {/* Primary: Cloud free trial */}
        <button
          onClick={handleCloud}
          aria-pressed={isCloudSelected}
          className={cn(
            'flex items-center gap-4 p-4 rounded-lg border-2 transition-all text-left',
            'hover:border-primary/50 hover:bg-muted/50',
            isCloudSelected
              ? 'border-primary bg-primary/10 ring-1 ring-primary/30'
              : 'border-border/50 bg-background',
          )}
        >
          <div className={cn(
            'flex-shrink-0 p-2 rounded-lg',
            isCloudSelected ? 'bg-primary/15 text-primary' : 'bg-muted text-muted-foreground',
          )}>
            <Cloud className="w-5 h-5" aria-hidden="true" />
          </div>
          <div className="min-w-0 flex-1 space-y-0.5">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm font-medium text-foreground">
                Use 20 free minutes of Reliant cloud compute
              </span>
              <span className="text-[10px] font-medium uppercase tracking-wider px-1.5 py-0.5 rounded bg-primary/20 text-primary">
                Recommended
              </span>
            </div>
            <span className="block text-xs text-muted-foreground">
              No installation required. Start coding immediately in the cloud.
            </span>
          </div>
          {isCloudSelected ? (
            <CheckCircle2 className="w-5 h-5 text-primary flex-shrink-0" aria-hidden="true" />
          ) : (
            <Circle className="w-5 h-5 text-muted-foreground/50 flex-shrink-0" aria-hidden="true" />
          )}
        </button>

        {/* Secondary: Local daemon */}
        <button
          onClick={handleLocal}
          aria-pressed={isLocalSelected}
          className={cn(
            'flex items-center gap-4 p-4 rounded-lg border-2 transition-all text-left',
            'hover:border-primary/50 hover:bg-muted/50',
            isLocalSelected
              ? 'border-primary bg-primary/10 ring-1 ring-primary/30'
              : 'border-border/50 bg-background',
          )}
        >
          <div className={cn(
            'flex-shrink-0 p-2 rounded-lg',
            isLocalSelected ? 'bg-primary/15 text-primary' : 'bg-muted text-muted-foreground',
          )}>
            <Cpu className="w-5 h-5" aria-hidden="true" />
          </div>
          <div className="min-w-0 flex-1 space-y-0.5">
            <span className="block text-sm font-medium text-foreground">
              Run locally on my machine
            </span>
            <span className="block text-xs text-muted-foreground">
              Download and install the Reliant daemon to run on your own hardware.
            </span>
          </div>
          {isLocalSelected ? (
            <CheckCircle2 className="w-5 h-5 text-primary flex-shrink-0" aria-hidden="true" />
          ) : (
            <Circle className="w-5 h-5 text-muted-foreground/50 flex-shrink-0" aria-hidden="true" />
          )}
        </button>
      </div>

      {/* Local daemon download section */}
      {showLocal && (
        <div className="space-y-4 rounded-lg border border-border/50 bg-muted/30 p-4">
          <h3 className="text-sm font-medium text-foreground">
            Install the Reliant daemon
          </h3>

          {/* Primary download for detected OS */}
          {primaryDownload ? (
            <div className="space-y-3">
              <a
                href={primaryDownload.url}
                className={cn(
                  'flex items-center justify-center gap-2 w-full py-3 rounded-lg text-sm font-semibold transition-colors border',
                  'border-border/60 bg-background text-foreground hover:bg-muted/70',
                )}
              >
                <Download className="w-4 h-4" aria-hidden="true" />
                Download for {primaryDownload.label}
              </a>

              <button
                onClick={() => setShowOtherPlatforms(!showOtherPlatforms)}
                className="inline-flex items-center justify-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors w-full text-center"
              >
                {showOtherPlatforms ? 'Hide' : 'Other platforms'}
                <ChevronDown className={cn(
                  'w-3.5 h-3.5 transition-transform',
                  showOtherPlatforms && 'rotate-180',
                )} aria-hidden="true" />
              </button>

              {showOtherPlatforms && (
                <div className="space-y-1.5">
                  {otherDownloads.map((link) => (
                    <a
                      key={link.url}
                      href={link.url}
                      className="flex items-center justify-between px-3 py-2 rounded text-xs text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
                    >
                      <span>{link.label}</span>
                      <Download className="w-3.5 h-3.5 text-primary" aria-hidden="true" />
                    </a>
                  ))}
                </div>
              )}
            </div>
          ) : (
            /* Unknown OS — show all download links */
            <div className="space-y-1.5">
              {DOWNLOAD_LINKS.map((link) => (
                <a
                  key={link.url}
                  href={link.url}
                  className="flex items-center justify-between px-3 py-2 rounded text-xs text-foreground hover:bg-muted/50 transition-colors border border-border/40"
                >
                  <span>{link.label}</span>
                  <Download className="w-3.5 h-3.5 text-primary" aria-hidden="true" />
                </a>
              ))}
            </div>
          )}

          {/* Homebrew */}
          <div className="space-y-1.5">
            <span className="block text-xs text-muted-foreground">Or install via Homebrew:</span>
            <code className="block text-xs bg-background border border-border/40 rounded px-3 py-2 text-foreground font-mono select-all">
              brew install --cask reliant-labs/reliant/reliant
            </code>
          </div>

          {/* Connection instructions */}
          <div className="space-y-1.5 pt-2 border-t border-border/30">
            <span className="block text-xs text-muted-foreground">After installing, run:</span>
            <code className="block text-xs bg-background border border-border/40 rounded px-3 py-2 text-foreground font-mono select-all">
              reliant daemon connect
            </code>
          </div>

          <button
            onClick={onNext}
            className={cn(
              'w-full py-2.5 rounded-lg text-sm font-medium transition-colors',
              'bg-primary text-primary-foreground hover:bg-primary/90',
            )}
          >
            Continue
          </button>
        </div>
      )}
    </div>
  );
}