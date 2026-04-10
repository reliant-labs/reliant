import { useState } from 'react';
import { cn } from '../../lib/utils';
import { ChevronDown, ChevronRight, AlertTriangle, RotateCw, Copy, Check } from 'lucide-react';
import type { ErrorUpdate } from '../../types/streaming';

interface WorkflowErrorMessageProps {
  error: ErrorUpdate;
}

export function WorkflowErrorMessage({ error }: WorkflowErrorMessageProps) {
  const [isExpanded, setIsExpanded] = useState(false);
  const [copied, setCopied] = useState(false);

  const isRetrying = error.is_retrying === true;

  // Format timestamp for display
  const formatTimestamp = (timestamp: string) => {
    try {
      const date = new Date(timestamp);
      return date.toLocaleTimeString('en-US', {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      });
    } catch {
      return timestamp;
    }
  };

  // Get a user-friendly display message. Prefer error_summary from backend,
  // fall back to client-side extraction, then generic label.
  const getDisplaySummary = () => {
    if (error.error_summary) {
      return error.error_summary;
    }
    // Client-side fallback: try to extract from the error_message
    const extracted = extractErrorSummaryClientSide(error.error_message);
    if (extracted) {
      return extracted;
    }
    // Generic fallback
    const activityName = error.activity_type.replace(/^V2_/, '').replace(/([A-Z])/g, ' $1').trim();
    return `Workflow error in ${activityName}`;
  };

  // Build retry status label like "Attempt 2/5"
  const getRetryLabel = () => {
    if (error.attempt_number && error.max_attempts) {
      return `Attempt ${error.attempt_number}/${error.max_attempts}`;
    }
    if (error.attempt_number) {
      return `Attempt ${error.attempt_number}`;
    }
    return null;
  };

  // Copy error details to clipboard
  const handleCopy = async () => {
    const lines = [
      getDisplaySummary(),
      '',
      `Timestamp: ${formatTimestamp(error.timestamp)}`,
    ];
    
    const retryLabel = getRetryLabel();
    if (retryLabel) {
      lines.push(retryLabel);
    }

    if (error.workflow_id) {
      lines.push(`Workflow ID: ${error.workflow_id}`);
    }
    
    lines.push('', 'Full Error:', error.error_message);
    
    try {
      await navigator.clipboard.writeText(lines.join('\n'));
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      console.error('Failed to copy error:', err);
    }
  };

  const summary = getDisplaySummary();
  const retryLabel = getRetryLabel();
  const Icon = isRetrying ? RotateCw : AlertTriangle;

  return (
    <div className={cn(
      "rounded-md border overflow-hidden my-2",
      isRetrying
        ? "border-warning/30 bg-warning/5"
        : "border-destructive/30 bg-destructive/5"
    )}>
      {/* Error Header - Always visible */}
      <div className={cn(
        "px-3 py-2 border-b cursor-pointer hover:elevation-1 transition-colors duration-200",
        isRetrying
          ? "border-warning/30 bg-warning/10"
          : "border-destructive/30 bg-destructive/10"
      )}>
        <button
          onClick={() => setIsExpanded(!isExpanded)}
          className="flex items-center gap-2 w-full text-left"
        >
          <Icon className={cn(
            "w-4 h-4 flex-shrink-0",
            isRetrying ? "text-warning animate-spin" : "text-destructive"
          )} data-testid={isRetrying ? "rotate-cw" : "alert-triangle"} />
          <div className="flex-1 min-w-0">
            <div className="text-sm font-medium text-foreground flex items-center gap-2">
              <span className="truncate">{summary}</span>
              {retryLabel && (
                <span className={cn(
                  "text-xs font-normal px-1.5 py-0.5 rounded-full whitespace-nowrap",
                  isRetrying
                    ? "bg-warning/20 text-warning"
                    : "bg-destructive/20 text-destructive"
                )}>
                  {isRetrying ? `Retrying (${retryLabel})` : retryLabel}
                </span>
              )}
            </div>
            <div className="text-xs text-muted-foreground">
              {formatTimestamp(error.timestamp)} · Click for details
            </div>
          </div>
          <div className="ml-auto flex items-center gap-1 flex-shrink-0">
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
        <div className="p-3 elevation-1 space-y-2">
          <div className="flex justify-end mb-2">
            <button
              onClick={handleCopy}
              className="flex items-center gap-1 px-2 py-1 text-xs rounded hover:bg-background/50 transition-colors text-muted-foreground"
              title={copied ? 'Copied!' : 'Copy error details'}
              aria-label={copied ? 'Copied!' : 'Copy error details'}
            >
              {copied ? (
                <>
                  <Check className="w-3.5 h-3.5 text-green-500" />
                  <span className="text-green-500">Copied</span>
                </>
              ) : (
                <>
                  <Copy className="w-3.5 h-3.5" />
                  <span>Copy</span>
                </>
              )}
            </button>
          </div>

          <div>
            <div className="text-xs text-muted-foreground mb-1 font-medium">
              Full Error:
            </div>
            <pre className="text-xs font-mono text-muted-foreground whitespace-pre-wrap break-words bg-background/50 p-2 rounded border border-border/50" data-sentry-mask>
              {error.error_message}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}

// Client-side fallback for extracting a friendly summary when the backend
// doesn't provide one (e.g. older error payloads already in the DB).
const API_ERROR_JSON_RE = /\{"type":"error","error":\{[^}]*"type":"([^"]+)","message":"([^"]+)"\}/;

const KNOWN_API_ERROR_TYPES: Record<string, string> = {
  overloaded_error: 'The AI provider is currently overloaded',
  rate_limit_error: 'Rate limited by the AI provider',
  api_error: 'AI provider internal server error',
  authentication_error: 'Authentication failed with the AI provider',
  permission_error: 'Permission denied by the AI provider',
  not_found_error: 'Model or resource not found at the AI provider',
  request_too_large: 'Request too large for the AI provider',
  invalid_request_error: 'Invalid request sent to the AI provider',
};

const KNOWN_ERROR_PATTERNS: Array<[string, string]> = [
  ['overloaded', 'The AI provider is currently overloaded'],
  ['rate limit', 'Rate limited by the AI provider'],
  ['too many requests', 'Rate limited by the AI provider'],
  ['internal server error', 'AI provider internal server error'],
  ['service unavailable', 'The AI provider is temporarily unavailable'],
  ['bad gateway', 'The AI provider returned a bad gateway error'],
  ['gateway timeout', 'The AI provider timed out'],
  ['timeout', 'Request to the AI provider timed out'],
  ['connection refused', 'Could not connect to the AI provider'],
  ['connection reset', 'Connection to the AI provider was reset'],
];

function extractProviderReconnectSummary(lower: string): string | null {
  if (lower.includes('claude session expired') || lower.includes('please reconnect claude')) {
    return 'Claude session expired. Please reconnect Claude. Workflow paused — send a message to retry.';
  }
  if (
    lower.includes('missing_scope') ||
    lower.includes('api.responses.write') ||
    lower.includes('codex token missing api.responses.write')
  ) {
    return 'Codex login is missing required API access. Disconnect Codex in Settings, then use Login with Codex again. Workflow paused — send a message to retry.';
  }
  if (
    lower.includes('codex session expired') ||
    lower.includes('please reconnect codex') ||
    lower.includes('codex authentication required')
  ) {
    return 'Codex session expired. Please reconnect Codex. Workflow paused — send a message to retry.';
  }
  if (
    lower.includes('api.anthropic.com') ||
    lower.includes('authentication_error') ||
    lower.includes('invalid authentication credentials')
  ) {
    return 'Claude session expired. Please reconnect Claude. Workflow paused — send a message to retry.';
  }
  return null;
}

function extractErrorSummaryClientSide(errMsg: string): string | null {
  const lower = errMsg.toLowerCase();
  const reconnectSummary = extractProviderReconnectSummary(lower);
  if (reconnectSummary) {
    return reconnectSummary;
  }

  // Try JSON payload extraction
  const match = API_ERROR_JSON_RE.exec(errMsg);
  if (match) {
    const [, errorType, errorMessage] = match;
    const friendly = KNOWN_API_ERROR_TYPES[errorType];
    if (friendly) {
      return `${friendly} (${errorMessage})`;
    }
    return `AI provider error: ${errorMessage}`;
  }

  // Fallback pattern matching
  for (const [pattern, summary] of KNOWN_ERROR_PATTERNS) {
    if (lower.includes(pattern)) {
      return summary;
    }
  }

  return null;
}