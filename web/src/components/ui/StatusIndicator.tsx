
import { cn } from '../../lib/utils';

export interface StatusIndicatorProps {
  status: 'online' | 'offline' | 'busy' | 'idle' | 'success' | 'error' | 'warning';
  size?: 'sm' | 'md' | 'lg';
  showPulse?: boolean;
  label?: string;
  className?: string;
}

const statusStyles = {
  online: 'bg-success border-success/30',
  offline: 'bg-muted-foreground border-muted-foreground/30',
  busy: 'bg-destructive border-destructive/30',
  idle: 'bg-warning border-warning/30',
  success: 'bg-success border-success/30',
  error: 'bg-destructive border-destructive/30',
  warning: 'bg-warning border-warning/30',
};

const statusSizes = {
  sm: 'w-2 h-2',
  md: 'w-3 h-3',
  lg: 'w-4 h-4',
};

export function StatusIndicator({
  status,
  size = 'md',
  showPulse = false,
  label,
  className,
}: StatusIndicatorProps) {
  const indicator = (
    <div className={cn('relative inline-flex', className)}>
      <div
        className={cn(
          'rounded-full border-2',
          statusStyles[status],
          statusSizes[size],
          showPulse && 'animate-pulse'
        )}
      />
      {showPulse && (
        <div
          className={cn(
            'absolute top-0 left-0 rounded-full border-2 opacity-75 animate-ping',
            statusStyles[status],
            statusSizes[size]
          )}
        />
      )}
    </div>
  );

  if (label) {
    return (
      <div className="flex items-center gap-2">
        {indicator}
        <span className="text-sm font-medium text-foreground">{label}</span>
      </div>
    );
  }

  return indicator;
}