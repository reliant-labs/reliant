// Copyright (c) 2025 Reliant Labs

/**
 * "Only from": the people a trigger runs for. A list of sender ids plus a
 * one-click "Me", written into the trigger's own CEL filter as
 *
 *   (<the rest of the filter>) && trigger.sender.verified && trigger.sender.id in [...]
 *
 * so there is one mechanism (the filter) and one place it is stored. The
 * list is read back only from exactly that shape (lib/onlyFromFilter); any
 * other use of trigger.sender is a hand-written filter, shown as custom and
 * left alone.
 *
 * Offered where the source verifies its senders: Slack (a signed request),
 * GitHub (a signed delivery) and Gmail (DMARC). Twilio cannot vouch for an
 * SMS sender, so it gets an explanation instead of a control that would
 * block every message.
 */

import { useId, useState, type KeyboardEvent } from "react";
import { UserCheck, X } from "lucide-react";

import { isOnlyFromIntegration, normalizeSenderId, parseOnlyFrom, withOnlyFrom, type OnlyFromIntegration } from "../../../lib/onlyFromFilter";
import { useConnectionSender, useGitHubSender, type MySender } from "../../../hooks/useMySenderId";
import { cn } from "../../../lib/utils";

export interface OnlyFromControlProps {
  /** The trigger's integration id ("slack", "github", "gmail", ...). */
  integration: string | undefined;
  /** The trigger's whole filter. */
  filter: string;
  /** Receives the whole new filter. */
  onChange: (filter: string) => void;
  disabled?: boolean;
  /** The connection the trigger listens through, when one is chosen: "Me" is read from it. */
  connectionId?: string;
  /** Field classes of the surrounding form; the config panel's by default. */
  classes?: { label?: string; input?: string; hint?: string };
}

const PLACEHOLDERS: Record<OnlyFromIntegration, string> = {
  slack: "Slack user id, like U0123ABCD",
  github: "GitHub login",
  gmail: "Email address",
};

const SOURCES: Record<OnlyFromIntegration, string> = {
  slack: "Slack",
  github: "GitHub",
  gmail: "Gmail (DMARC)",
};

export function OnlyFromControl({ integration, filter, onChange, disabled, connectionId, classes }: OnlyFromControlProps) {
  const ids = useId();
  const labelClass = classes?.label ?? "cpv2-field-label";
  const inputClass = classes?.input ?? "cpv2-field-input";
  const hintClass = classes?.hint ?? "cpv2-field-hint";

  if (integration === "twilio") {
    return (
      <div>
        <div className={labelClass}>Only from</div>
        <p className={cn(hintClass, "!mt-0")}>
          Twilio can't verify who sent a message (SMS caller ID can be spoofed), so there is no allowlist here. A filter on{" "}
          <code className="font-mono">trigger.sender.id</code> can narrow by number, but it doesn't prove who sent it.
        </p>
      </div>
    );
  }
  if (!isOnlyFromIntegration(integration)) return null;

  const state = parseOnlyFrom(filter);
  const listed = state.kind === "list" ? state.ids : [];
  const setIds = (next: string[]) => {
    const written = withOnlyFrom(filter, next);
    if (written !== null) onChange(written);
  };
  const add = (raw: string) => {
    const id = normalizeSenderId(integration, raw);
    if (!id || listed.includes(id)) return;
    setIds([...listed, id]);
  };
  const row: AddRowProps = { integration, listed, disabled, onAdd: add, inputClass, hintClass, inputId: `${ids}-input` };

  return (
    <div role="group" aria-labelledby={`${ids}-label`}>
      <div className={labelClass}>
        <span id={`${ids}-label`}>Only from</span>
      </div>
      {state.kind === "custom" ? (
        <p className={cn(hintClass, "!mt-0")}>
          Custom filter: it already decides who this runs for, in a shape this list can't show. Edit it in the filter.
        </p>
      ) : (
        <>
          {listed.length > 0 ? (
            <ul className="mb-2 flex flex-wrap gap-1.5" aria-label="Allowed senders">
              {listed.map((id) => (
                <li key={id} className="inline-flex items-center gap-1 rounded-md border border-border/60 bg-background px-2 py-0.5 font-mono text-xs text-foreground">
                  {id}
                  <button
                    type="button"
                    onClick={() => setIds(listed.filter((other) => other !== id))}
                    disabled={disabled}
                    aria-label={`Remove ${id}`}
                    className="rounded-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
                  >
                    <X className="h-3 w-3" aria-hidden />
                  </button>
                </li>
              ))}
            </ul>
          ) : (
            <p className={cn(hintClass, "!mt-0 mb-2")}>Anyone. Add people to run only for them.</p>
          )}
          {integration === "github" ? (
            <GitHubAddRow {...row} connectionId={connectionId} />
          ) : (
            <ConnectionAddRow {...row} connectionId={connectionId} />
          )}
          {listed.length > 0 && (
            <p className={hintClass}>
              Only events {SOURCES[integration]} verified as sent by someone on this list start a run; the rest are recorded as skipped.
            </p>
          )}
        </>
      )}
    </div>
  );
}

interface AddRowProps {
  integration: OnlyFromIntegration;
  listed: readonly string[];
  disabled?: boolean;
  onAdd: (raw: string) => void;
  inputClass: string;
  hintClass: string;
  inputId: string;
  connectionId?: string;
}

// "Me" is looked up per integration; GitHub's needs a second source (the
// hosted account), so each is its own component rather than a branch inside
// one hook.
function ConnectionAddRow(props: AddRowProps) {
  return <AddRow {...props} me={useConnectionSender(props.integration, props.connectionId)} />;
}

function GitHubAddRow(props: AddRowProps) {
  return <AddRow {...props} me={useGitHubSender(props.connectionId)} />;
}

const buttonClass =
  "inline-flex items-center gap-1 rounded-md border border-border px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50";

function AddRow({ integration, listed, disabled, onAdd, inputClass, hintClass, inputId, me }: AddRowProps & { me: MySender }) {
  const [draft, setDraft] = useState("");
  const submit = () => {
    onAdd(draft);
    setDraft("");
  };
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter") {
      event.preventDefault();
      submit();
    }
  };
  const meListed = !!me.id && listed.includes(me.id);
  return (
    <>
      <div className="flex items-center gap-1.5">
        <input
          id={inputId}
          aria-label="Add a sender"
          value={draft}
          disabled={disabled}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder={PLACEHOLDERS[integration]}
          className={cn(inputClass, "min-w-0 flex-1 font-mono")}
          autoComplete="off"
          spellCheck={false}
        />
        <button type="button" onClick={submit} disabled={disabled || !draft.trim()} className={buttonClass}>
          Add
        </button>
        <button
          type="button"
          onClick={() => me.id && onAdd(me.id)}
          disabled={disabled || !me.id || meListed}
          aria-label={me.id ? `Add me (${me.id})` : "Add me"}
          className={buttonClass}
        >
          <UserCheck className="h-3.5 w-3.5" aria-hidden />
          Me
        </button>
      </div>
      {!me.loading && me.missing && <p className={hintClass}>{me.missing}</p>}
    </>
  );
}
