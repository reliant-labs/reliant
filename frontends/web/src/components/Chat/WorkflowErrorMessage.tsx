import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import Tooltip from "../forge-ui/tooltip";
import { cn } from '../../lib/utils';
import { ChevronRight, AlertTriangle, RotateCw, Copy, Check, ArrowUpRight } from 'lucide-react';
import type { ErrorUpdate } from '../../types/streaming';
import { cleanTemporalErrorMessage, hasTemporalScaffolding } from '../../lib/temporalErrors';
import { settingsPath } from "@/lib/paths";

interface WorkflowErrorMessageProps {
  error: ErrorUpdate;
}

// Phrases that mean "the fix is in Settings → Providers": a model no connected
// provider can serve (internal/llm/models.ProviderUnavailableError.Explain), a
// provider sign-in that must be redone, or no provider at all. Matched on what
// the backend and extractProviderReconnectSummary below actually emit.
const PROVIDER_SETTINGS_SIGNALS = [
  'settings → providers',
  'none of required providers',
  'please reconnect',
  'then use login with',
  'no api keys configured',
  'authentication failed with the ai provider',
];

/**
 * Whether this error is fixed by connecting or reconnecting a provider, so the
 * card can offer to go there instead of only describing the way.
 */
export function needsProviderSettings(summary: string, message: string): boolean {
  const text = `${summary}\n${message}`.toLowerCase();
  return PROVIDER_SETTINGS_SIGNALS.some((signal) => text.includes(signal));
}

function OpenProviderSettingsButton() {
  const navigate = useNavigate();
  return (
    <button
      type="button"
      onClick={() =>
        // The AI section opens on its Providers tab.
        navigate({ to: settingsPath('ai') })
      }
      className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-primary px-3 text-ui font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      Open Settings → Providers
      <ArrowUpRight className="size-3.5" />
    </button>
  );
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
  // Only once the run has stopped: while it is still retrying the error may
  // clear on its own.
  const offerProviderSettings = !isRetrying && needsProviderSettings(summary, cleanedMessage);

  const details = (
    <div className="space-y-3">
      <pre className="whitespace-pre-wrap break-words font-mono text-code text-muted-foreground">
        {cleanedMessage}
      </pre>
      <div className="flex items-center gap-3">
        <Tooltip content={copied ? 'Copied!' : 'Copy error details'} side="bottom" delay={300} triggerClassName="inline-flex">
          <button
            type="button"
            onClick={handleCopy}
            className="inline-flex items-center gap-1.5 rounded-md text-ui text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            aria-label={copied ? 'Copied!' : 'Copy error details'}
          >
            {copied ? <Check className="size-3.5 text-success-ink" /> : <Copy className="size-3.5" />}
            <span>{copied ? 'Copied' : 'Copy'}</span>
          </button>
        </Tooltip>
        {canShowRaw && (
          <button
            type="button"
            onClick={() => setShowRaw(!showRaw)}
            className="text-ui text-muted-foreground transition-colors hover:text-foreground"
          >
            {showRaw ? 'Hide raw error' : 'Show raw error'}
          </button>
        )}
      </div>
      {canShowRaw && showRaw && (
        <pre className="whitespace-pre-wrap break-words font-mono text-code text-muted-foreground">
          {error.error_message}
        </pre>
      )}
    </div>
  );

  const disclosure = (
    <button
      type="button"
      onClick={() => setIsExpanded(!isExpanded)}
      aria-expanded={isExpanded}
      aria-label={`${summary} at ${timestampLabel}. Click for details`}
      className={cn(
        'group/err flex w-full min-w-0 items-center gap-2 rounded-md text-left text-ui transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:min-h-[44px]',
        isRetrying ? 'min-h-7 text-muted-foreground hover:text-foreground' : 'font-medium text-foreground',
      )}
    >
      <span className="inline-flex size-4 shrink-0 items-center justify-center">
        <Icon
          className={cn('size-4', isRetrying ? 'text-warning-ink motion-safe:animate-spin' : 'text-destructive-ink')}
          data-testid={isRetrying ? 'rotate-cw' : 'alert-triangle'}
        />
      </span>
      <span className="min-w-0 flex-1 truncate">{summary}</span>
      {retryLabel && (
        <span className="shrink-0 whitespace-nowrap text-meta font-normal text-muted-foreground">
          {isRetrying ? `Retrying (${retryLabel})` : retryLabel}
        </span>
      )}
      <span
        className="shrink-0 whitespace-nowrap text-meta font-normal tabular-nums text-muted-foreground max-sm:hidden"
        data-testid="workflow-error-timestamp"
      >
        {timestampLabel}
      </span>
      <ChevronRight
        className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', isExpanded && 'rotate-90')}
        data-testid="workflow-error-chevron"
      />
    </button>
  );

  // A provider still being retried is a quiet row: it may clear on its own.
  if (isRetrying) {
    return (
      <div className="min-w-0">
        {disclosure}
        {isExpanded && <div className="mb-3 ml-2 mt-1 border-l border-border-subtle pl-4">{details}</div>}
      </div>
    );
  }

  // The failure that stopped the turn is a card: what went wrong, and the
  // way out when there is one.
  return (
    <div className="rounded-xl border border-border p-4 sm:p-5" data-testid="workflow-error-card">
      {disclosure}
      {offerProviderSettings && (
        <div className="mt-3 pl-6">
          <OpenProviderSettingsButton />
        </div>
      )}
      {isExpanded && <div className="mt-3 pl-6">{details}</div>}
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