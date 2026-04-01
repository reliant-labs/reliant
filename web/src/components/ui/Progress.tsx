
import { cn } from '../../lib/utils';

export interface ProgressProps {
  value: number; // 0-100
  variant?: 'default' | 'primary' | 'success' | 'warning' | 'destructive';
  size?: 'sm' | 'md' | 'lg';
  showLabel?: boolean;
  label?: string;
  className?: string;
}

const progressVariants = {
  default: 'bg-primary',
  primary: 'bg-primary',
  success: 'bg-success',
  warning: 'bg-warning',
  destructive: 'bg-destructive',
};

const progressSizes = {
  sm: 'h-1',
  md: 'h-2',
  lg: 'h-3',
};

export function Progress({
  value,
  variant = 'primary',
  size = 'md',
  showLabel = false,
  label,
  className,
}: ProgressProps) {
  const clampedValue = Math.min(100, Math.max(0, value));
  
  return (
    <div className={cn('w-full', className)}>
      {(showLabel || label) && (
        <div className="flex justify-between items-center mb-2">
          <span className="text-sm font-medium text-foreground">
            {label || 'Progress'}
          </span>
          {showLabel && (
            <span className="text-sm text-muted-foreground font-mono">
              {Math.round(clampedValue)}%
            </span>
          )}
        </div>
      )}
      
      <div className={cn(
        'w-full bg-muted rounded-full overflow-hidden',
        progressSizes[size]
      )}>
        <div
          className={cn(
            'transition-all duration-500 ease-out rounded-full',
            progressVariants[variant],
            progressSizes[size]
          )}
          style={{
            width: `${clampedValue}%`,
          }}
        />
      </div>
    </div>
  );
}