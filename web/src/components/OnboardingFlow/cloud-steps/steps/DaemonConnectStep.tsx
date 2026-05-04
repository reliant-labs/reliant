import { Check } from 'lucide-react';
import { useState, useEffect, useRef } from 'react';
import { cn } from '../../../../lib/utils';
import type { StepProps } from '../../types';
import { getControlPlaneClient } from '../api';
import { DaemonService, DaemonStatus } from '../gen/admin_pb';

type ChecklistItemStatus = 'done' | 'active' | 'pending';

interface ChecklistItem {
  label: string;
  status: ChecklistItemStatus;
}

function StatusIcon({ status }: { status: ChecklistItemStatus }) {
  if (status === 'done') {
    return <Check className="w-3.5 h-3.5 text-green-500" aria-hidden="true" />;
  }
  if (status === 'active') {
    return (
      <span className="inline-block w-3.5 h-3.5 border-2 border-primary border-t-transparent rounded-full animate-spin" />
    );
  }
  return <span className="inline-block w-3 h-3 rounded-full border-2 border-border/60" />;
}

export function DaemonConnectStep({ onNext, onSkip }: StepProps) {
  const [connected, setConnected] = useState(false);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Poll for daemon connection
  useEffect(() => {
    const client = getControlPlaneClient(DaemonService);

    const pollDaemonStatus = async () => {
      try {
        const resp = await client.listDaemons({});
        const hasActive = resp.daemons.some(
          (d) => d.status === DaemonStatus.ACTIVE,
        );
        if (hasActive) setConnected(true);
      } catch {
        // Silently retry on failure
      }
    };

    intervalRef.current = setInterval(pollDaemonStatus, 3000);
    pollDaemonStatus();

    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, []);

  // Auto-advance when connected
  useEffect(() => {
    if (connected) {
      const timer = setTimeout(onNext, 800);
      return () => clearTimeout(timer);
    }
  }, [connected, onNext]);

  const checklist: ChecklistItem[] = [
    { label: 'Selecting workflow', status: 'done' },
    { label: 'Preparing environment', status: 'done' },
    { label: 'Connecting daemon', status: connected ? 'done' : 'active' },
  ];

  return (
    <div className="space-y-6">
      <div className="text-center space-y-2">
        <h2 className="text-xl font-semibold text-foreground">
          Setting up your workspace
        </h2>
        <p className="text-sm text-muted-foreground">
          {connected
            ? 'Daemon connected! Moving on...'
            : 'Waiting for your local daemon to connect...'}
        </p>
      </div>

      <div className="rounded-lg border border-border/50 bg-muted/30 px-4 py-3 space-y-3">
        {checklist.map((item) => (
          <div key={item.label} className="flex items-center gap-3">
            <StatusIcon status={item.status} />
            <span
              className={cn(
                'text-sm',
                item.status === 'done'
                  ? 'text-foreground'
                  : item.status === 'active'
                    ? 'text-foreground font-medium'
                    : 'text-muted-foreground',
              )}
            >
              {item.label}
              {item.status === 'active' && (
                <span className="text-xs text-muted-foreground ml-2">(polling...)</span>
              )}
            </span>
          </div>
        ))}
      </div>

      {!connected && (
        <div className="space-y-3">
          <div className="rounded-lg border border-border/40 bg-background p-3 space-y-1.5">
            <span className="block text-xs text-muted-foreground">
              Make sure you've run this command in your terminal:
            </span>
            <code className="block text-xs font-mono text-foreground select-all">
              reliant daemon connect
            </code>
          </div>

          <button
            onClick={onSkip}
            className="w-full text-center text-xs text-muted-foreground hover:text-foreground transition-colors py-2"
          >
            Skip for now — I'll connect later
          </button>
        </div>
      )}
    </div>
  );
}