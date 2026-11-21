// Copyright (c) 2025 Reliant Labs

/**
 * The field a Vertex AI account is connected with: a Google Cloud
 * service-account key file, pasted whole into a multi-line box, with the steps
 * to make one beside it. Every surface that takes a Vertex AI credential uses
 * it (the API key dialog, Settings › Providers, and an org's AI accounts), so
 * the instructions are said once.
 *
 * The key is never shown back after it is saved, and nothing here logs it. A
 * textarea cannot mask its text, so it is only on screen while it is pasted.
 */

import Textarea from "@/components/forge-ui/textarea";
import { cn } from "../../../lib/utils";

export const SERVICE_ACCOUNTS_URL = "https://console.cloud.google.com/iam-admin/serviceaccounts";

const PLACEHOLDER = `{
  "type": "service_account",
  "project_id": "…",
  "private_key": "…",
  "client_email": "…@….iam.gserviceaccount.com",
  …
}`;

export interface ServiceAccountKeyFieldProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  /** Why the pasted text cannot be a key (serviceAccountKeyError), shown under the field. */
  error?: string | null;
  label?: string;
  className?: string;
  "data-testid"?: string;
}

export function ServiceAccountKeyField({
  id,
  value,
  onChange,
  error,
  label = "Service-account key (JSON)",
  className,
  "data-testid": testId,
}: ServiceAccountKeyFieldProps) {
  const helpId = `${id}-help`;
  const errorId = `${id}-error`;
  return (
    <div className={cn("space-y-1.5", className)}>
      <label htmlFor={id} className="block text-sm font-medium text-foreground">
        {label}
      </label>
      <Textarea
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={PLACEHOLDER}
        rows={6}
        textareaSize="sm"
        className="font-mono"
        autoComplete="off"
        autoCapitalize="off"
        autoCorrect="off"
        spellCheck={false}
        invalid={Boolean(error)}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? `${helpId} ${errorId}` : helpId}
        data-testid={testId}
      />
      {error && (
        <p id={errorId} role="alert" className="text-xs text-destructive-ink" data-testid="service-account-key-error">
          {error}
        </p>
      )}
      <ol id={helpId} className="list-decimal space-y-0.5 pl-4 text-xs text-muted-foreground">
        <li>
          In Google Cloud, create a service account with the role <span className="text-foreground">Vertex AI User</span>{" "}
          (<code className="font-mono">roles/aiplatform.user</code>).
        </li>
        <li>
          Open{" "}
          <a href={SERVICE_ACCOUNTS_URL} target="_blank" rel="noopener noreferrer" className="text-primary-ink hover:underline">
            IAM › Service accounts
          </a>
          , choose the account, then Keys › Add key › Create new key › JSON.
        </li>
        <li>Paste the whole downloaded file here. Calls run in the key&apos;s project.</li>
      </ol>
    </div>
  );
}
