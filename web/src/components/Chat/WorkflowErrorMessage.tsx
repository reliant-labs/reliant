import { useState } from 'react';
import { cn } from '../../lib/utils';
import { ChevronDown, ChevronRight, AlertTriangle, RotateCw, Copy, Check } from 'lucide-react';
import type { ErrorUpdate } from '../../types/streaming';
import { cleanTemporalErrorMessage, hasTemporalScaffolding } from '../../lib/temporalErrors';

interface WorkflowErrorMessageProps {
  error: ErrorUpdate;
}

export function WorkflowErrorMessage({ error }: WorkflowErrorMessageProps) {
  const [isExpanded, setIsExpanded] = useState(false);
  const [showRaw, setShowRaw] = useState(false);
  const [copied, setCopied] = useState(false);

  const isRetrying = error.is_retrying === true;

  // The detail pane shows the causal chain with Temporal's event bookkeeping
  // removed; the untouched string stays one click away for bug reports.
  const cleanedMessage = cleanTemporalErrorMessage(error.error_message);
  const canShowRaw = hasTemporalScaffolding(error.error_message);

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
    const extracted = extractErrorSummaryClientSide(cleanedMessage);
    if (extracted) {
      return extracted;
    }
    // Generic fallback
    const activityName = error.activity_type
      .replace(/^V2_/, '')
      // Split CamelCase without exploding acronyms: "CallLLM" -> "Call LLM",
      // "LLMCallHandler" -> "LLM Call Handler".
      .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
      .replace(/([A-Z]+)([A-Z][a-z])/g, '$1 $2')
      .trim();
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
    
    lines.push('', 'Error:', cleanedMessage);
    if (canShowRaw) {
      lines.push('', 'Raw error:', error.error_message);
    }
    
    try {
      await navigator.clipboard.writeText(lines.join('\n'));
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      console.error('Failed to copy error:', err);
    }
  };

  const summary = getDisplaySummary();
  const timestampLabel = formatTimestamp(error.timestamp);
  const retryLabel = getRetryLabel();
  const Icon = isRetrying ? RotateCw : AlertTriangle;

  return (
    <div className={cn(
      "rounded-md border overflow-hidden my-1",
      isRetrying
        ? "border-warning/30 bg-warning/5"
        : "border-destructive/30 bg-destructive/5"
    )}>
      {/* Error header — one line, always visible. The border-b only appears
          when expanded; collapsed, the background shift already separates it. */}
      <button
        onClick={() => setIsExpanded(!isExpanded)}
        aria-expanded={isExpanded}
        aria-label={`${summary} at ${timestampLabel}. Click for details`}
        className={cn(
          "flex items-center gap-1.5 w-full text-left px-2 py-1 transition-colors duration-200 hover:elevation-1",
          isExpanded && "border-b",
          isRetrying
            ? "border-warning/30 bg-warning/10"
            : "border-destructive/30 bg-destructive/10"
        )}
      >
        <Icon className={cn(
          "w-3.5 h-3.5 flex-shrink-0",
          isRetrying ? "text-warning animate-spin" : "text-destructive"
        )} data-testid={isRetrying ? "rotate-cw" : "alert-triangle"} />
        <span className="text-xs font-medium text-foreground truncate min-w-0 flex-1">
          {summary}
        </span>
        {retryLabel && (
          <span className={cn(
            "text-xs font-normal px-1.5 rounded-full whitespace-nowrap flex-shrink-0",
            isRetrying
              ? "bg-warning/20 text-warning"
              : "bg-destructive/20 text-destructive"
          )}>
            {isRetrying ? `Retrying (${retryLabel})` : retryLabel}
          </span>
        )}
        <span
          className="text-xs text-muted-foreground whitespace-nowrap flex-shrink-0"
          data-testid="workflow-error-timestamp"
        >
          {timestampLabel}
        </span>
        {isExpanded ? (
          <ChevronDown className="w-3.5 h-3.5 text-muted-foreground flex-shrink-0" data-testid="workflow-error-chevron" />
        ) : (
          <ChevronRight className="w-3.5 h-3.5 text-muted-foreground flex-shrink-0" data-testid="workflow-error-chevron" />
        )}
      </button>

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
              Error:
            </div>
            <pre className="text-xs font-mono text-muted-foreground whitespace-pre-wrap break-words bg-background/50 p-2 rounded border border-border/50" data-sentry-mask>
              {cleanedMessage}
            </pre>
          </div>

          {canShowRaw && (
            <div>
              <button
                onClick={() => setShowRaw(!showRaw)}
                className="text-xs text-muted-foreground hover:text-foreground transition-colors"
              >
                {showRaw ? 'Hide raw error' : 'Show raw error'}
              </button>
              {showRaw && (
                <pre className="mt-1 text-xs font-mono text-muted-foreground whitespace-pre-wrap break-words bg-background/50 p-2 rounded border border-border/50" data-sentry-mask>
                  {error.error_message}
                </pre>
              )}
            </div>
          )}
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
  // 'connection refused' / 'connection reset' are absent deliberately: they are
  // transport failures, matched earlier by NETWORK_FAILURE_SIGNALS.
];

// A 401 against Codex's responses API is a Codex credential problem however the
// body is worded. Matching the host (rather than a bare "401") keeps another
// provider's unauthorized error from being labelled as Codex's.
const CODEX_BACKEND_HOST = 'chatgpt.com/backend-api/codex';

// Mirrors networkFailureSummary in internal/workflow/runtime/error_summary.go,
// with the paused-workflow suffix every summary on this path carries.
const NETWORK_FAILURE_SUMMARY =
  'Cannot reach the AI provider — check your network connection. Workflow paused — send a message to retry.';

// These appear only when the request failed below HTTP — the socket was never
// opened, or died before a response arrived. Any one alone is conclusive.
const NETWORK_FAILURE_SIGNALS = [
  'no such host',
  'dial tcp',
  'dial udp',
  'connection refused',
  'connection reset',
  'broken pipe',
  'i/o timeout',
  'network is unreachable',
  'no route to host',
  'tls handshake timeout',
];

// These name the syscall that failed. They qualify errors that are ambiguous on
// their own — "context deadline exceeded" is a plain request timeout elsewhere.
const TRANSPORT_LAYER_SIGNALS = ['dial tcp', 'dial udp', 'read tcp', 'write tcp'];

// Reports whether the transport never produced a usable HTTP response. Must be
// consulted before any provider-specific matching: a provider's hostname
// appears in the URL of every failed request to it, so matching on the hostname
// alone reports DNS and connectivity outages as expired credentials and sends
// users to re-authenticate for no reason.
function isNetworkFailure(lower: string): boolean {
  if (NETWORK_FAILURE_SIGNALS.some((signal) => lower.includes(signal))) {
    return true;
  }

  // A truncated stream reads as EOF. Anchor on the delimiter so an unrelated
  // word that merely ends in those three letters cannot match.
  if (lower.includes('unexpected eof') || lower.includes(': eof') || lower.endsWith(' eof')) {
    return true;
  }

  if (lower.includes('context deadline exceeded')) {
    return TRANSPORT_LAYER_SIGNALS.some((signal) => lower.includes(signal));
  }

  return false;
}

function extractProviderReconnectSummary(lower: string): string | null {
  const unauthorized = lower.includes('401 unauthorized');
  const tokenExpired = lower.includes('token_expired');

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
  if (lower.includes(CODEX_BACKEND_HOST) && (unauthorized || tokenExpired)) {
    return 'Codex session expired. Please reconnect Codex. Workflow paused — send a message to retry.';
  }
  if (
    lower.includes('api.anthropic.com') ||
    lower.includes('authentication_error') ||
    lower.includes('invalid authentication credentials')
  ) {
    return 'Claude session expired. Please reconnect Claude. Workflow paused — send a message to retry.';
  }
  if (tokenExpired) {
    return 'Authentication token expired. Please reconnect your AI provider. Workflow paused — send a message to retry.';
  }
  if (unauthorized) {
    return 'Authentication failed with the AI provider. Workflow paused — send a message to retry.';
  }
  return null;
}

function extractErrorSummaryClientSide(errMsg: string): string | null {
  const lower = errMsg.toLowerCase();

  // Before anything else: if the request never reached the provider, no
  // provider-specific claim about it can be true.
  if (isNetworkFailure(lower)) {
    return NETWORK_FAILURE_SUMMARY;
  }

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