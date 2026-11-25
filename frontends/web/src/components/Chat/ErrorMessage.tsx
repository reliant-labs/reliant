import Tooltip from "../forge-ui/tooltip";
import { formatErrorMessage } from '../../lib/utils';
import { AlertCircle, AlertTriangle, X } from 'lucide-react';
import { ErrorRow, ERROR_ROW_ACTION_CLASS } from './ErrorRow';

interface ErrorMessageProps {
  content: string;
  onDismiss?: () => void;
}

/**
 * An assistant text segment that is an error ("Error: …"): one quiet row with
 * the simplified message, the full text one click away.
 */
export function ErrorMessage({ content, onDismiss }: ErrorMessageProps) {
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
    if (isCritical) return <AlertTriangle className="size-4 text-destructive-ink" data-testid="alert-triangle" />;
    if (isAuthError || isClientError) return <AlertCircle className="size-4 text-warning-ink" data-testid="alert-circle" />;
    return <AlertCircle className="size-4 text-destructive-ink" data-testid="alert-circle" />;
  };

  return (
    <ErrorRow
      icon={getErrorIcon()}
      details={
        <pre className="whitespace-pre-wrap break-words font-mono text-code text-muted-foreground">{fullError}</pre>
      }
      actions={
        onDismiss && (
          <Tooltip content="Dismiss error" side="bottom" delay={300} triggerClassName="inline-flex">
            <button type="button" onClick={onDismiss} className={ERROR_ROW_ACTION_CLASS} aria-label="Dismiss error">
              <X className="size-3.5" />
            </button>
          </Tooltip>
        )
      }
      data-testid="error-message-row"
    >
      {simplifiedError}
    </ErrorRow>
  );
}
