import { useState, useMemo } from 'react';
import { cn } from '../../../../lib/utils';
import type { StepProps, ComputeChoice } from '../../types';

type DetectedOS = 'mac-arm64' | 'mac-x64' | 'windows' | 'linux' | 'unknown';

interface DownloadLink {
  label: string;
  url: string;
  os: DetectedOS;
}

const DOWNLOAD_LINKS: DownloadLink[] = [
  { label: 'Mac (Apple Silicon)', url: 'https://downloads.reliantlabs.io/Reliant-latest-mac-arm64.dmg', os: 'mac-arm64' },
  { label: 'Mac (Intel)', url: 'https://downloads.reliantlabs.io/Reliant-latest-mac-x64.dmg', os: 'mac-x64' },
  { label: 'Windows x64', url: 'https://downloads.reliantlabs.io/Reliant-latest-win-x64.exe', os: 'windows' },
  { label: 'Windows ARM64', url: 'https://downloads.reliantlabs.io/Reliant-latest-win-arm64.exe', os: 'windows' },
  { label: 'Linux x86_64', url: 'https://downloads.reliantlabs.io/Reliant-latest-linux-x86_64.AppImage', os: 'linux' },
  { label: 'Linux ARM64', url: 'https://downloads.reliantlabs.io/Reliant-latest-linux-arm64.AppImage', os: 'linux' },
];

function getOS(): DetectedOS {
  const platform = navigator.platform;
  if (/Mac/i.test(platform)) {
    return (navigator as any).userAgentData?.architecture === 'arm'
      ? 'mac-arm64'
      : 'mac-x64';
  }
  if (/Win/i.test(platform)) return 'windows';
  if (/Linux/i.test(platform)) return 'linux';
  return 'unknown';
}

function getPrimaryDownload(os: DetectedOS): DownloadLink | null {
  if (os === 'unknown') return null;
  // Return the first match for the detected OS
  return DOWNLOAD_LINKS.find((l) => l.os === os) ?? null;
}

export function ComputeStep({ plan, updatePlan, onNext }: StepProps) {
  const [showLocal, setShowLocal] = useState(plan.compute === 'local_daemon');
  const [showOtherPlatforms, setShowOtherPlatforms] = useState(false);

  const detectedOS = useMemo(() => getOS(), []);
  const primaryDownload = useMemo(() => getPrimaryDownload(detectedOS), [detectedOS]);
  const otherDownloads = useMemo(
    () => DOWNLOAD_LINKS.filter((l) => l !== primaryDownload),
    [primaryDownload],
  );

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
          className={cn(
            'flex items-center gap-4 p-5 rounded-lg border-2 transition-all text-left',
            'hover:border-primary/50 hover:bg-muted/50',
            plan.compute === 'cloud_free_trial'
              ? 'border-primary bg-primary/10'
              : 'border-primary/30 bg-primary/5',
          )}
        >
          <span className="text-3xl shrink-0" role="img" aria-label="Cloud">
            ☁️
          </span>
          <div className="space-y-1 flex-1">
            <div className="flex items-center gap-2">
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
        </button>

        {/* Secondary: Local daemon */}
        <button
          onClick={handleLocal}
          className={cn(
            'flex items-center gap-4 p-4 rounded-lg border-2 transition-all text-left',
            'hover:border-primary/50 hover:bg-muted/50',
            plan.compute === 'local_daemon'
              ? 'border-primary bg-primary/10'
              : 'border-border/40 bg-background',
          )}
        >
          <span className="text-2xl shrink-0" role="img" aria-label="Local">
            💻
          </span>
          <div className="space-y-0.5">
            <span className="block text-sm font-medium text-foreground">
              Run locally on my machine
            </span>
            <span className="block text-xs text-muted-foreground">
              Download and install the Reliant daemon to run on your own hardware.
            </span>
          </div>
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
                  'flex items-center justify-center gap-2 w-full py-3 rounded-lg text-sm font-semibold transition-colors',
                  'bg-primary text-primary-foreground hover:bg-primary/90',
                )}
              >
                Download for {primaryDownload.label}
              </a>

              <button
                onClick={() => setShowOtherPlatforms(!showOtherPlatforms)}
                className="text-xs text-muted-foreground hover:text-foreground transition-colors w-full text-center"
              >
                {showOtherPlatforms ? 'Hide' : 'Other platforms'} ▾
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
                      <span className="text-primary">↓</span>
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
                  <span className="text-primary">↓</span>
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