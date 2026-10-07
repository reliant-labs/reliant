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
 * The list stores ids and shows people. On GitHub the id is the numeric user
 * id, because a login can be renamed and then registered by someone else: a
 * login typed here is resolved to its id when it is added (as the caller,
 * through GitHub), and each id is shown by its login (hooks/useSenderNames).
 * A login left in a GitHub list from before matches nobody, and is flagged.
 *
 * Offered where the source verifies its senders: Slack (a signed request),
 * GitHub (a signed delivery) and Gmail (DMARC). Twilio cannot vouch for an
 * SMS sender, so it gets an explanation instead of a control that would
 * block every message.
 */

import { useId, useState, type KeyboardEvent } from "react";
import { UserCheck, X } from "lucide-react";

import {
  gitHubLogin,
  isGitHubUserId,
  isOnlyFromIntegration,
  normalizeSenderId,
  parseOnlyFrom,
  withOnlyFrom,
  type OnlyFromIntegration,
} from "../../../lib/onlyFromFilter";
import { triggerErrorMessage, triggerGrpc } from "../../../api/trigger-grpc";
import { useConnectionSender, useGitHubSender, type MySender } from "../../../hooks/useMySenderId";
import { useSenderNames, type SenderNames } from "../../../hooks/useSenderNames";
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
  /** The stored trigger, when there is one: its recorded firings name the people on the list. */
  triggerId?: string;
  /** Field classes of the surrounding form; the config panel's by default. */
  classes?: { label?: string; input?: string; hint?: string };
}

const PLACEHOLDERS: Record<OnlyFromIntegration, string> = {
  slack: "Slack user id, like U0123ABCD",
  github: "GitHub login, like octocat",
  gmail: "Email address",
};

const SOURCES: Record<OnlyFromIntegration, string> = {
  slack: "Slack",
  github: "GitHub",
  gmail: "Gmail (DMARC)",
};

export function OnlyFromControl({ integration, filter, onChange, disabled, connectionId, triggerId, classes }: OnlyFromControlProps) {
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
  const list: ListProps = {
    integration,
    listed: state.kind === "list" ? state.ids : [],
    setIds: (next) => {
      const written = withOnlyFrom(filter, next);
      if (written !== null) onChange(written);
    },
    disabled,
    triggerId,
    inputClass,
    hintClass,
    inputId: `${ids}-input`,
  };

  return (
    <div role="group" aria-labelledby={`${ids}-label`}>
      <div className={labelClass}>
        <span id={`${ids}-label`}>Only from</span>
      </div>
      {state.kind === "custom" ? (
        <p className={cn(hintClass, "!mt-0")}>
          Custom filter: it already decides who this runs for, in a shape this list can't show. Edit it in the filter.
        </p>
      ) : integration === "github" ? (
        <GitHubList {...list} connectionId={connectionId} />
      ) : (
        <ConnectionList {...list} connectionId={connectionId} />
      )}
    </div>
  );
}

interface ListProps {
  integration: OnlyFromIntegration;
  listed: readonly string[];
  setIds: (ids: string[]) => void;
  disabled?: boolean;
  triggerId?: string;
  inputClass: string;
  hintClass: string;
  inputId: string;
  connectionId?: string;
}

/** What an Add resolves typed text to: the id to store, or why there is none. */
type Resolution = { id: string } | { error: string };

// "Me" is looked up per integration; GitHub's needs a second source (the
// hosted account), so each is its own component rather than a branch inside
// one hook.
function ConnectionList(props: ListProps) {
  const me = useConnectionSender(props.integration, props.connectionId);
  // A Slack user id or an email address is already the id: nothing to ask.
  const resolve = (raw: string): Resolution => ({ id: normalizeSenderId(props.integration, raw) });
  return <SenderList {...props} me={me} resolve={resolve} />;
}

function GitHubList(props: ListProps) {
  const me = useGitHubSender(props.connectionId);
  // A login is resolved to its user id now, as the caller, so the list holds
  // the person GitHub says the login names today — never the login itself.
  const resolve = async (raw: string, names: SenderNames): Promise<Resolution> => {
    const login = gitHubLogin(raw);
    if (!login) return { error: "" };
    try {
      const [found] = await triggerGrpc.resolveSenders("github", { handles: [login] });
      if (!found) return { error: `No GitHub user is named ${login}.` };
      names.remember(found.senderId, found.displayName);
      return { id: found.senderId };
    } catch (error) {
      return { error: triggerErrorMessage(error) };
    }
  };
  return <SenderList {...props} me={me} resolve={resolve} />;
}

interface Chip {
  label: string;
  title?: string;
  /** An id shown as itself rather than a name. */
  raw: boolean;
  /** A GitHub login from before senders were user ids: it matches nobody. */
  stale: boolean;
}

function chipFor(integration: OnlyFromIntegration, id: string, names: SenderNames): Chip {
  if (integration === "github") {
    if (!isGitHubUserId(id)) {
      return {
        label: `@${id}`,
        title: "A GitHub login saved before people were matched by user id. It matches nobody: remove it and add the person again.",
        raw: false,
        stale: true,
      };
    }
    const name = names.nameOf(id);
    return { label: name ?? `GitHub user ${id}`, title: `GitHub user id ${id}`, raw: !name, stale: false };
  }
  const name = names.nameOf(id);
  return { label: name ?? id, title: name ? id : undefined, raw: !name, stale: false };
}

const buttonClass =
  "inline-flex items-center gap-1 rounded-md border border-border px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50";

function SenderList({
  integration,
  listed,
  setIds,
  disabled,
  triggerId,
  inputClass,
  hintClass,
  inputId,
  me,
  resolve,
}: ListProps & { me: MySender; resolve: (raw: string, names: SenderNames) => Resolution | Promise<Resolution> }) {
  const names = useSenderNames(integration, listed, { triggerId, me });
  const [draft, setDraft] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");

  const add = (id: string) => {
    if (id && !listed.includes(id)) setIds([...listed, id]);
  };
  const submit = async () => {
    if (!draft.trim() || pending) return;
    setError("");
    let result = resolve(draft, names);
    if (result instanceof Promise) {
      setPending(true);
      result = await result;
      setPending(false);
    }
    if ("error" in result) {
      setError(result.error);
      return;
    }
    add(result.id);
    setDraft("");
  };
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter") {
      event.preventDefault();
      void submit();
    }
  };

  const chips = listed.map((id) => ({ id, ...chipFor(integration, id, names) }));
  const meListed = !!me.id && listed.includes(me.id);
  const meName = me.displayName ?? me.id;

  return (
    <>
      {chips.length > 0 ? (
        <ul className="mb-2 flex flex-wrap gap-1.5" aria-label="Allowed senders">
          {chips.map((chip) => (
            <li
              key={chip.id}
              title={chip.title}
              className={cn(
                "inline-flex items-center gap-1 rounded-md border bg-background px-2 py-0.5 text-xs",
                chip.stale ? "border-destructive/60 text-destructive" : "border-border/60 text-foreground",
                chip.raw && "font-mono",
              )}
            >
              {chip.label}
              <button
                type="button"
                onClick={() => setIds(listed.filter((other) => other !== chip.id))}
                disabled={disabled}
                aria-label={`Remove ${chip.label}`}
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
      <div className="flex items-center gap-1.5">
        <input
          id={inputId}
          aria-label="Add a sender"
          value={draft}
          disabled={disabled}
          onChange={(e) => {
            setDraft(e.target.value);
            setError("");
          }}
          onKeyDown={onKeyDown}
          placeholder={PLACEHOLDERS[integration]}
          className={cn(inputClass, "min-w-0 flex-1 font-mono")}
          autoComplete="off"
          spellCheck={false}
        />
        <button type="button" onClick={() => void submit()} disabled={disabled || pending || !draft.trim()} className={buttonClass}>
          {pending ? "Looking up…" : "Add"}
        </button>
        <button
          type="button"
          onClick={() => me.id && add(me.id)}
          disabled={disabled || !me.id || meListed}
          aria-label={meName ? `Add me (${meName})` : "Add me"}
          className={buttonClass}
        >
          <UserCheck className="h-3.5 w-3.5" aria-hidden />
          Me
        </button>
      </div>
      {error && (
        <p role="alert" className={cn(hintClass, "text-destructive")}>
          {error}
        </p>
      )}
      {!me.loading && me.missing && <p className={hintClass}>{me.missing}</p>}
      {chips.some((chip) => chip.stale) && (
        <p className={cn(hintClass, "text-destructive")}>
          GitHub people are matched by user id now, so the logins marked here match nobody. Remove them and add the people again.
        </p>
      )}
      {listed.length > 0 && (
        <p className={hintClass}>
          Only events {SOURCES[integration]} verified as sent by someone on this list start a run; the rest are recorded as skipped.
        </p>
      )}
    </>
  );
}
