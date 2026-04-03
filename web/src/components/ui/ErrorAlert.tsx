import { AlertCircle, X } from 'lucide-react';
import { cn } from '../../lib/utils';

interface ErrorAlertProps {
  error: string | null;
  onDismiss?: () => void;
  className?: string;
  variant?: 'inline' | 'modal' | 'toast';
}

export function ErrorAlert({ 
  error, 
  onDismiss, 
  className = "",
  variant = 'inline'
}: ErrorAlertProps) {
  if (!error) return null;

  if (variant === 'toast') {
    return (
      <div className={cn(
        "fixed bottom-4 right-4 z-50 max-w-md",
        "animate-in slide-in-from-bottom-2 fade-in duration-300",
        className
      )}>
        <div className="bg-destructive/90 text-destructive-foreground backdrop-blur-md rounded-lg elevation-4 p-4 border border-destructive/30">
          <div className="flex items-start gap-3">
            <AlertCircle className="w-5 h-5 flex-shrink-0 mt-0.5" />
            <div className="flex-1">
              <p className="text-sm font-medium">Error</p>
              <p className="text-sm mt-1 opacity-90">{error}</p>
            </div>
            {onDismiss && (
              <button
                onClick={onDismiss}
                className="p-1 hover:bg-destructive-foreground/10 rounded transition-colors"
              >
                <X className="w-4 h-4" />
              </button>
            )}
          </div>
        </div>
      </div>
    );
  }

  if (variant === 'modal') {
    // Check if error contains commands (has 'gh' or looks like a command)
    const hasCommand = error.includes('gh ') || error.includes('GH_TOKEN');
    
    return (
      <div className={cn(
        "bg-destructive/10 border border-destructive/20 rounded-lg p-3",
        className
      )}>
        <div className="flex items-start gap-2">
          <AlertCircle className="w-4 h-4 text-destructive flex-shrink-0 mt-0.5" />
          <div className="flex-1">
            {hasCommand ? (
              <pre className="text-sm text-destructive whitespace-pre-wrap font-mono" data-sentry-mask>
                {error}
              </pre>
            ) : (
              <p className="text-sm text-destructive" data-sentry-mask>{error}</p>
            )}
          </div>
          {onDismiss && (
            <button
              onClick={onDismiss}
              className="p-0.5 hover:bg-destructive/10 rounded transition-colors"
            >
              <X className="w-3 h-3 text-destructive" />
            </button>
          )}
        </div>
      </div>
    );
  }

  // Default inline variant
  return (
    <div className={cn(
      "bg-destructive/10 text-destructive rounded text-xs font-mono p-2",
      className
    )}>
      <div className="flex items-center gap-2">
        <AlertCircle className="w-3 h-3 flex-shrink-0" />
        <span className="flex-1">{error}</span>
        {onDismiss && (
          <button
            onClick={onDismiss}
            className="p-0.5 hover:bg-destructive/20 rounded transition-colors"
          >
            <X className="w-3 h-3" />
          </button>
        )}
      </div>
    </div>
  );
}