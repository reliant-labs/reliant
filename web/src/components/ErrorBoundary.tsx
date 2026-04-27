import React, { Component } from 'react';
import type { ReactNode } from 'react';
import * as Sentry from '@sentry/react';
import { AlertTriangle, Copy, RefreshCw } from 'lucide-react';
import { getPrivacySettings } from '../store/privacyStore';
import { Button } from './ui/Button';
import { isDev } from '../lib/constants';

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
}

interface State {
  hasError: boolean;
  error?: Error;
  componentStack?: string;
  errorId?: string;
  occurredAt?: string;
}

function createErrorId() {
  // short, human-readable id for support/debugging
  return Math.random().toString(36).slice(2, 8).toUpperCase();
}

function formatDiagnosticReport(params: {
  error: unknown;
  componentStack?: string;
  errorId?: string;
  occurredAt?: string;
}) {
  const { error, componentStack, errorId, occurredAt } = params;

  const err = error instanceof Error ? error : new Error(String(error));
  const lines: string[] = [];

  lines.push('Reliant - Error Report');
  if (errorId) lines.push(`Error ID: ${errorId}`);
  if (occurredAt) lines.push(`Occurred At: ${occurredAt}`);
  lines.push(`URL: ${typeof window !== 'undefined' ? window.location.href : ''}`);
  lines.push(`User Agent: ${typeof navigator !== 'undefined' ? navigator.userAgent : ''}`);
  lines.push('');
  lines.push('Message:');
  lines.push(`${err.name}: ${err.message}`);
  lines.push('');
  lines.push('Stack:');
  lines.push(err.stack ?? '(no stack)');

  if (componentStack) {
    lines.push('');
    lines.push('React Component Stack:');
    lines.push(componentStack.trim());
  }

  return lines.join('\n');
}

async function copyToClipboard(text: string) {
  // Prefer async clipboard when available.
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }

  // Fallback for older environments.
  const el = document.createElement('textarea');
  el.value = text;
  el.setAttribute('readonly', 'true');
  el.style.position = 'fixed';
  el.style.top = '0';
  el.style.left = '0';
  el.style.opacity = '0';
  document.body.appendChild(el);
  el.select();
  document.execCommand('copy');
  document.body.removeChild(el);
}

export function ErrorFallbackUI(props: {
  title?: string;
  description?: string;
  error: unknown;
  componentStack?: string;
  errorId?: string;
  occurredAt?: string;
  onReload: () => void;
}) {
  const {
    title = 'Something went wrong',
    description =
      'An unexpected error occurred. Reload the app. If it keeps happening, report the problem so we can investigate.',
    error,
    componentStack,
    errorId,
    occurredAt,
    onReload,
  } = props;

  const [copied, setCopied] = React.useState(false);

  const err = error instanceof Error ? error : new Error(String(error));
  const report = formatDiagnosticReport({ error, componentStack, errorId, occurredAt });

  return (
    <div
      className="min-h-screen flex items-center justify-center bg-background px-4 py-10"
      role="alert"
      aria-live="assertive"
    >
      <div className="w-full max-w-2xl rounded-xl border border-border/60 bg-card elevation-4 p-6">
        <div className="flex items-start gap-4">
          <div className="mt-0.5 flex h-12 w-12 items-center justify-center rounded-full bg-destructive/10 text-destructive">
            <AlertTriangle className="h-6 w-6" />
          </div>

          <div className="flex-1">
            <h1 className="text-xl font-semibold text-foreground">{title}</h1>
            <p className="mt-1 text-sm text-muted-foreground">{description}</p>

            <div className="mt-4 flex flex-wrap gap-2">
              <Button
                variant="primary"
                onClick={onReload}
                leftIcon={<RefreshCw className="h-4 w-4" />}
              >
                Reload
              </Button>
              <Button
                variant="secondary"
                onClick={async () => {
                  await copyToClipboard(report);
                  setCopied(true);
                  window.setTimeout(() => setCopied(false), 1500);
                }}
                leftIcon={<Copy className="h-4 w-4" />}
              >
                {copied ? 'Copied' : 'Copy details'}
              </Button>
            </div>

            <div className="mt-4 rounded-lg border border-border/60 bg-muted/30 p-3">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                {errorId ? (
                  <span>
                    <span className="font-medium text-foreground">Error ID:</span> {errorId}
                  </span>
                ) : null}
                {occurredAt ? (
                  <span>
                    <span className="font-medium text-foreground">Time:</span> {occurredAt}
                  </span>
                ) : null}
              </div>
              <div className="mt-2 font-mono text-xs text-destructive break-words">
                {err.name}: {err.message}
              </div>
            </div>

            <details className="mt-4" open={isDev}>
              <summary className="cursor-pointer select-none text-sm text-muted-foreground hover:text-foreground">
                Technical details
              </summary>

              {!isDev ? (
                <p className="mt-2 text-xs text-muted-foreground">
                  These details may include internal stack traces. Copy and share them only with someone you trust.
                </p>
              ) : null}

              <pre className="mt-2 max-h-[45vh] overflow-auto rounded-md border border-border/60 bg-background p-3 text-xs leading-relaxed text-foreground">
                {report}
              </pre>
            </details>
          </div>
        </div>
      </div>
    </div>
  );
}

class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, errorInfo: React.ErrorInfo) {
    console.error('Error caught by boundary:', error, errorInfo);

    // Store component stack for diagnostics UI/copy.
    this.setState({
      componentStack: errorInfo.componentStack ?? undefined,
      errorId: this.state.errorId ?? createErrorId(),
      occurredAt: this.state.occurredAt ?? new Date().toISOString(),
    });
    
    // Only report to Sentry if user has crash reporting enabled
    const { crashReportingEnabled } = getPrivacySettings();
    if (crashReportingEnabled) {
      Sentry.captureException(error, {
        contexts: {
          react: {
            componentStack: errorInfo.componentStack,
          },
        },
      });
    }
  }

  render() {
    if (this.state.hasError) {
      if (this.props.fallback) {
        return this.props.fallback;
      }

      return (
        <ErrorFallbackUI
          error={this.state.error}
          componentStack={this.state.componentStack}
          errorId={this.state.errorId}
          occurredAt={this.state.occurredAt}
          onReload={() => window.location.reload()}
        />
      );
    }

    return this.props.children;
  }
}

export const SentryErrorBoundary = Sentry.withErrorBoundary(
  ({ children }: { children: ReactNode }) => <>{children}</>,
  {
    fallback: ({ error }) => (
      <ErrorFallbackUI
        error={error}
        onReload={() => window.location.reload()}
        // Note: Sentry boundary fallback doesn't provide React component stack.
        // We still provide a rich diagnostic report (message/stack/url/user agent).
        errorId={createErrorId()}
        occurredAt={new Date().toISOString()}
      />
    ),
    showDialog: isDev,
  }
);

export default ErrorBoundary;