import { Activity } from 'lucide-react';
import { GradientBackground } from '../GradientBackground';
import { Tooltip } from '../ui/Tooltip';
import { isDev } from '../../lib/constants';
import { openExternalLink } from '../../lib/open-link';
import { BrandMark } from '../icons/BrandMark';

export function LoadingSpinner() {
  const isMac = window.electronAPI?.platform === 'darwin';

  return (
    <div className="fixed inset-0 bg-background flex flex-col">
      <style>{`
        @keyframes spin {
          to { transform: rotate(360deg); }
        }
      `}</style>

      {/* Gradient Background */}
      <GradientBackground />

      {/* Draggable header area */}
      <div
        className="h-12 border-b border-border bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60 relative z-20 flex items-center justify-between"
        style={{
          WebkitAppRegion: 'drag',
          WebkitUserSelect: 'none',
          userSelect: 'none',
          paddingLeft: isMac ? '80px' : '12px'
        } as React.CSSProperties}
      >
        {/* Dev-only: Temporal UI button */}
        {isDev && (
          <div
            className="pr-2"
            style={{ WebkitAppRegion: 'no-drag' } as React.CSSProperties}
          >
            <Tooltip content="Open Temporal UI" placement="bottom" delay={300}>
              <button
                onClick={() => {
                  const temporalUIPort = window.RELIANT_CONFIG?.temporalUIPort || 8233;
                  const temporalUIUrl = `http://localhost:${temporalUIPort}`;
                  void openExternalLink(temporalUIUrl);
                }}
                className="p-1.5 hover:bg-accent rounded text-xs transition-colors"
                aria-label="Open Temporal UI"
              >
                <Activity className="w-4 h-4" />
              </button>
            </Tooltip>
          </div>
        )}
      </div>

      {/* Loading content */}
      <div className="flex-1 flex items-center justify-center relative z-10">
        <div className="relative w-32 h-32 flex items-center justify-center">
          <svg 
            className="absolute inset-0 w-full h-full" 
            viewBox="0 0 100 100" 
            style={{ animation: "spin 1.5s linear infinite" }}
            aria-hidden="true"
            focusable="false"
          >
            <defs>
              <linearGradient id="gradient-ring" x1="0%" y1="0%" x2="100%" y2="100%">
                <stop offset="0%" stopColor="hsl(var(--primary))" />
                <stop offset="50%" stopColor="hsl(var(--secondary))" />
                <stop offset="100%" stopColor="hsl(var(--primary))" stopOpacity="0" />
              </linearGradient>
            </defs>
            <circle
              cx="50"
              cy="50"
              r="45"
              fill="none"
              stroke="url(#gradient-ring)"
              strokeWidth="2.5"
              strokeLinecap="round"
            />
          </svg>
          <BrandMark className="w-16 h-16" />
        </div>
      </div>
    </div>
  );
}