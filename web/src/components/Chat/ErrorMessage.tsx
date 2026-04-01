import { useState } from 'react';
import { cn, formatErrorMessage } from '../../lib/utils';
import { ChevronDown,ChevronRight,AlertCircle,AlertTriangle,X } from 'lucide-react';
interface ErrorMessageProps {
  content: string;
  onDismiss?: () => void;
}

export function ErrorMessage({ content, onDismiss }: ErrorMessageProps) {
  const [isExpanded, setIsExpanded] = useState(false);

  // Extract the error message from the content
  const errorMatch = content.match(/^Error:\s*(.+)/i);
  if (!errorMatch) {
    // If it doesn't start with "Error:", render as regular content
    return <div className="text-sm text-muted-foreground">{content}</div>;
  }

  const fullError = errorMatch[1];
  const simplifiedError = formatErrorMessage(fullError);

  // Determine error severity based on content
  const isCritical = content.includes('500') || content.includes('502') || content.includes('503');
  const isAuthError = content.includes('401') || content.includes('403');
  const isClientError = content.includes('400') || content.includes('429');

  const getErrorIcon = () => {
    if (isCritical) return <AlertTriangle className="w-4 h-4 text-destructive" data-testid="alert-triangle" />;
    if (isAuthError) return <AlertCircle className="w-4 h-4 text-warning" data-testid="alert-circle" />;
    if (isClientError) return <AlertCircle className="w-4 h-4 text-warning" data-testid="alert-circle" />;
    return <AlertCircle className="w-4 h-4 text-destructive" data-testid="alert-circle" />;
  };

  const getErrorColor = () => {
    if (isCritical) return 'border-destructive/30 bg-destructive/5';
    if (isAuthError) return 'border-warning/30 bg-warning/5';
    if (isClientError) return 'border-warning/30 bg-warning/5';
    return 'border-destructive/30 bg-destructive/5';
  };

  const getHeaderColor = () => {
    if (isCritical) return 'border-destructive/30 bg-destructive/10';
    if (isAuthError) return 'border-warning/30 bg-warning/10';
    if (isClientError) return 'border-warning/30 bg-warning/10';
    return 'border-destructive/30 bg-destructive/10';
  };

  return (
    <div className={cn(
      "rounded-md border overflow-hidden",
      getErrorColor()
    )}>
      {/* Error Header - Always visible */}
      <div className={cn(
        "px-3 py-2 border-b cursor-pointer hover:elevation-1 transition-colors duration-200",
        getHeaderColor()
      )}>
        <button
          onClick={() => {
            setIsExpanded((prev) => !prev);
          }}
          className="flex items-center gap-2 w-full text-left"
        >
          {getErrorIcon()}
          <span className="text-sm font-medium text-foreground">
            {simplifiedError}
          </span>
          <div className="ml-auto flex items-center gap-1">
            {onDismiss && (
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  onDismiss();
                }}
                className="p-1 hover:bg-background/50 rounded transition-colors"
                title="Dismiss error"
              >
                <X className="w-3 h-3 text-muted-foreground" />
              </button>
            )}
            {isExpanded ? (
              <ChevronDown className="w-4 h-4 text-muted-foreground" />
            ) : (
              <ChevronRight className="w-4 h-4 text-muted-foreground" />
            )}
          </div>
        </button>
      </div>

      {/* Expandable Error Details */}
      {isExpanded && (
        <div className="p-3 elevation-1">
          <div className="text-xs text-muted-foreground mb-2 font-medium">
            Technical Details:
          </div>
          <pre className="text-xs font-mono text-muted-foreground whitespace-pre-wrap break-words bg-background/50 p-2 rounded border border-border/50">
            {fullError}
          </pre>
        </div>
      )}
    </div>
  );
}
