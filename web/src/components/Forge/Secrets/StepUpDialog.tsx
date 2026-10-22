// Copyright (c) 2025 Reliant Labs

/**
 * The step-up dialog: request a code, enter it, and the refused change is
 * saved. Every phase is designed (services/forge/stepUp.ts names them).
 *
 * ── TWO HOSTS, ONE BODY ─────────────────────────────────────────────────────
 *
 * `StepUpPanel` is the body. When the refused write came from the set-secret
 * form, it renders INSIDE that form's modal, in place of the form: the value
 * the user typed stays in the form's state underneath and is what the retry
 * saves. Every other write (delete, undelete, destroy) and the header's
 * "Confirm to edit" use `StepUpDialog`, the same body in its own modal. Two
 * modals stacked would both answer Escape and both trap focus.
 *
 * ── WHAT IT SAYS ────────────────────────────────────────────────────────────
 *
 * The register is the form's: a calm inset for explanation, destructive ink
 * only for a code that was refused. A protected environment asking for a code
 * is the system working, not an error — the intro says what the code buys
 * (fifteen minutes, this environment) so the user is not asked again for
 * every change.
 */

import { useEffect, useId, useState } from "react";
import { AlertTriangle, Loader2, Lock, Mail } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import { cn } from "@/lib/utils";
import {
  STEP_UP_CODE_LENGTH,
  attemptsLeft,
  canSend,
  canSubmit,
  codeExpiresInMs,
  codeOf,
  formatCountdown,
  resendWaitMs,
  type StepUpState,
} from "@/services/forge/stepUp";

import type { StepUpGate } from "./useStepUpGate";

export interface StepUpPanelProps {
  gate: StepUpGate;
  /** The environment being confirmed for. An identifier: mono. */
  env: string;
}

export function StepUpPanel({ gate, env }: StepUpPanelProps) {
  const { state, now } = gate;
  const codeId = useId();
  const [code, setCode] = useState("");
  const current = codeOf(state);

  // A new code (or a closed dialog) clears the field: the old code no longer
  // works, and leaving it there invites a sixth attempt at a dead code.
  const sentAt = current?.sentAt ?? null;
  useEffect(() => setCode(""), [sentAt]);
  useEffect(() => {
    if (state.phase === "closed") setCode("");
  }, [state.phase]);

  const busy = state.phase === "sending" || state.phase === "verifying" || state.phase === "retrying";

  return (
    <div className="space-y-5" data-testid="step-up" data-phase={state.phase}>
      <Lead state={state} env={env} hasPendingWrite={gate.hasPendingWrite} now={now} />

      {(state.phase === "entering" || state.phase === "verifying") && (
        <form
          className="space-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            gate.submit(code);
          }}
          id={`${codeId}-form`}
        >
          <label htmlFor={codeId} className="block text-sm font-medium text-foreground">
            Code
          </label>
          <input
            id={codeId}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            disabled={state.phase === "verifying"}
            autoFocus
            /* The browser's one-time-code hint: offers the emailed code where
               the platform can, and never offers to save it. */
            autoComplete="one-time-code"
            spellCheck={false}
            autoCapitalize="characters"
            maxLength={STEP_UP_CODE_LENGTH + 6}
            placeholder={"X".repeat(STEP_UP_CODE_LENGTH)}
            data-testid="step-up-code"
            aria-describedby={`${codeId}-hint`}
            aria-invalid={state.phase === "entering" && state.notice === "invalid"}
            className={cn(
              "w-full rounded-md border bg-background px-3 py-2 font-mono text-sm uppercase tracking-[0.2em] text-foreground",
              "placeholder:text-muted-foreground/40",
              "focus:outline-none focus:ring-2 focus:ring-primary/40",
              "disabled:cursor-not-allowed disabled:text-muted-foreground",
              state.phase === "entering" && state.notice === "invalid" ? "border-destructive/60" : "border-border"
            )}
          />
          <p id={`${codeId}-hint`} className="text-xs text-muted-foreground">
            {STEP_UP_CODE_LENGTH} characters. Case, spaces and dashes don&apos;t matter.
          </p>
        </form>
      )}

      <Notice state={state} />

      <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border pt-4">
        <ResendButton state={state} now={now} onSend={gate.send} />
        <span className="flex-1" />
        {state.phase !== "retrying" && (
          <button
            type="button"
            onClick={gate.cancel}
            data-testid="step-up-cancel"
            className="rounded-md px-3 py-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
          >
            {state.phase === "failed" ? "Close" : "Cancel"}
          </button>
        )}
        <PrimaryAction state={state} code={code} busy={busy} now={now} onSend={gate.send} formId={`${codeId}-form`} />
      </div>
    </div>
  );
}

/** The sentence at the top: what is happening, in this phase. */
function Lead({
  state,
  env,
  hasPendingWrite,
  now,
}: {
  state: StepUpState;
  env: string;
  hasPendingWrite: boolean;
  now: number;
}) {
  const code = codeOf(state);
  if (state.phase === "failed") {
    return (
      <Inset icon={Lock} testId="step-up-failed">
        {state.reason === "forbidden" ? (
          <>
            Only an organization admin can change secrets in{" "}
            <span className="font-mono text-foreground">{env}</span>, because it is protected. Ask an
            admin to make this change.
          </>
        ) : (
          <>
            <span className="font-mono text-foreground">{env}</span> is protected, and this deployment
            cannot send confirmation codes, so its secrets are read-only here. Nothing was changed.
          </>
        )}
      </Inset>
    );
  }
  if (state.phase === "retrying") {
    return (
      <p className="flex items-center gap-2 text-sm text-foreground" data-testid="step-up-retrying">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" aria-hidden="true" />
        Confirmed. Saving your change…
      </p>
    );
  }
  if (state.phase === "intro" || (state.phase === "sending" && !code)) {
    return (
      <Inset icon={Lock} testId="step-up-intro">
        <span className="text-foreground">
          <span className="font-mono">{env}</span> is protected.
        </span>{" "}
        Changing its secrets needs a one-time code, sent to the email on your account. Once you
        confirm, you can change secrets here for 15 minutes.
        {hasPendingWrite && " Your change has not been saved yet — it will be once you confirm."}
      </Inset>
    );
  }
  if (!code) return null;
  const expiresIn = codeExpiresInMs(code, now);
  return (
    <Inset icon={Mail} testId="step-up-sent">
      {code.sentTo ? (
        <>
          We sent a code to <span className="font-mono text-foreground">{code.sentTo}</span>.
        </>
      ) : (
        <>A code was sent to the email on your account a moment ago.</>
      )}{" "}
      {state.phase === "expired" || state.phase === "exhausted"
        ? null
        : expiresIn !== null && (
            <>
              It expires in{" "}
              <span className="tabular-nums text-foreground" data-testid="step-up-expires-in">
                {formatCountdown(expiresIn)}
              </span>
              .
            </>
          )}
      {hasPendingWrite && state.phase !== "expired" && state.phase !== "exhausted" && (
        <> Your change is saved as soon as the code is confirmed.</>
      )}
    </Inset>
  );
}

/** At most one line about the last action. */
function Notice({ state }: { state: StepUpState }) {
  if (state.phase === "intro" && state.notice === "send-failed") {
    return <ErrorLine testId="step-up-send-failed">The code could not be sent. Try again.</ErrorLine>;
  }
  if (state.phase === "exhausted") {
    return (
      <ErrorLine testId="step-up-exhausted">
        This code has been tried 5 times and can no longer be used. Send a new code to try again.
      </ErrorLine>
    );
  }
  if (state.phase === "expired") {
    return (
      <ErrorLine testId="step-up-expired">This code has expired. Send a new code to try again.</ErrorLine>
    );
  }
  if (state.phase !== "entering" || !state.notice) return null;
  const left = attemptsLeft(state.code);
  const remaining = `${left} ${left === 1 ? "attempt" : "attempts"} left on this code.`;
  switch (state.notice) {
    case "invalid":
      return <ErrorLine testId="step-up-invalid">That code was not accepted. {remaining}</ErrorLine>;
    case "network":
      return (
        <ErrorLine testId="step-up-network">
          The code could not be checked — the connection failed. {remaining}
        </ErrorLine>
      );
    case "send-failed":
      return (
        <ErrorLine testId="step-up-send-failed">
          A new code could not be sent. The one you already have still works.
        </ErrorLine>
      );
    case "rate-limited":
      // Not an error: the code from a moment ago is in the inbox and good.
      return (
        <p className="text-xs text-muted-foreground" data-testid="step-up-rate-limited">
          A code was sent less than a minute ago, so no new one was sent. Use the code from that email.
        </p>
      );
  }
}

/** "Send a new code", counting down to when the server will allow one. */
function ResendButton({ state, now, onSend }: { state: StepUpState; now: number; onSend: () => void }) {
  if (state.phase !== "entering" && state.phase !== "verifying") return null;
  const wait = resendWaitMs(codeOf(state), now);
  return (
    <button
      type="button"
      onClick={onSend}
      disabled={!canSend(state, now)}
      data-testid="step-up-resend"
      className="rounded-md px-1 py-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground disabled:cursor-not-allowed disabled:opacity-60"
    >
      {wait > 0 ? (
        <>
          Send a new code in <span className="tabular-nums">{formatCountdown(wait)}</span>
        </>
      ) : (
        "Send a new code"
      )}
    </button>
  );
}

function PrimaryAction({
  state,
  code,
  busy,
  now,
  onSend,
  formId,
}: {
  state: StepUpState;
  code: string;
  busy: boolean;
  now: number;
  onSend: () => void;
  formId: string;
}) {
  const className = cn(
    "inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
    "bg-primary text-primary-foreground hover:bg-primary/90",
    "disabled:cursor-not-allowed disabled:opacity-50"
  );
  switch (state.phase) {
    case "intro":
    case "sending":
      return (
        <button type="button" onClick={onSend} disabled={busy} data-testid="step-up-send" className={className}>
          {state.phase === "sending" && <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />}
          {state.phase === "sending" ? "Sending…" : "Email me a code"}
        </button>
      );
    case "entering":
    case "verifying":
      return (
        <button
          type="submit"
          form={formId}
          disabled={busy || !canSubmit(state, code)}
          data-testid="step-up-confirm"
          className={className}
        >
          {state.phase === "verifying" && <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />}
          {state.phase === "verifying" ? "Confirming…" : "Confirm"}
        </button>
      );
    case "exhausted":
    case "expired": {
      // After a dead code, a new one is the only way on — so it is the
      // primary action, counting down when the server would refuse it.
      const wait = resendWaitMs(codeOf(state), now);
      return (
        <button
          type="button"
          onClick={onSend}
          disabled={!canSend(state, now)}
          data-testid="step-up-send"
          className={className}
        >
          {/* One text node: the button is a flex row, and a separate span
              would wrap onto its own line. */}
          <span className="tabular-nums">
            {wait > 0 ? `Send a new code in ${formatCountdown(wait)}` : "Send a new code"}
          </span>
        </button>
      );
    }
    default:
      return null;
  }
}

function Inset({
  icon: Icon,
  testId,
  children,
}: {
  icon: typeof Lock;
  testId: string;
  children: React.ReactNode;
}) {
  // An inset on the modal surface: bg-background + border-border/60 per the
  // elevation rules, never bg-muted.
  return (
    <div data-testid={testId} className="flex gap-2.5 rounded-md border border-border/60 bg-background px-3 py-2.5">
      <Icon className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      <p className="text-xs leading-relaxed text-muted-foreground">{children}</p>
    </div>
  );
}

function ErrorLine({ testId, children }: { testId: string; children: React.ReactNode }) {
  return (
    <p role="alert" data-testid={testId} className="flex gap-2 text-xs leading-relaxed text-destructive-ink">
      <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span>{children}</span>
    </p>
  );
}

/** The panel in its own modal, for every write that is not the set-secret form's. */
export function StepUpDialog({ gate, env, open }: StepUpPanelProps & { open: boolean }) {
  return (
    <Modal
      open={open}
      onClose={gate.cancel}
      size="md"
      title={`Confirm it's you`}
      // A misclick outside must not throw away a code the user is halfway
      // through typing.
      closeOnOverlay={false}
    >
      <StepUpPanel gate={gate} env={env} />
    </Modal>
  );
}
