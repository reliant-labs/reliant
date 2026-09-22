// Copyright (c) 2025 Reliant Labs

/**
 * The create/set flow. "secrets i have no way to create them?" — this is that.
 *
 * ── MAKING WRITE-ONLY LEGIBLE RATHER THAN MYSTERIOUS ────────────────────────
 *
 * The value field submits and never renders back. That is not a limitation this
 * form is apologising for; it is the product's central property, and a user who
 * does not understand it will come back tomorrow looking for a reveal button,
 * conclude the feature is broken, and store the value somewhere worse.
 *
 * So the form says so ONCE, plainly, next to the field it applies to — not in a
 * legend at the top of the page, which is the pattern the user rejected
 * outright. And it says WHY in terms of a fact rather than a policy: the store
 * refuses reads to this service, so there is no reveal to build. A user told
 * "we chose not to show you this" reasonably asks for the setting that turns it
 * on. A user told "the credential we hold cannot read it" understands there is
 * no such setting.
 *
 * ── autoComplete="off" AND type="password" ─────────────────────────────────
 *
 * Both, for different reasons. `type="password"` keeps the value off the screen
 * while typing, which matters on a shared screen. `autoComplete="new-password"`
 * stops the browser offering to SAVE it into a password manager keyed to
 * reliant's origin — a secret that belongs in the managed store would otherwise
 * be silently copied into a second store nobody audits.
 *
 * ── CAS: HOW CREATE DIFFERS FROM UPDATE ─────────────────────────────────────
 *
 * KV-v2's check-and-set is the mechanism, and it is passed explicitly rather
 * than letting the write be unconditional:
 *
 *   creating   cas = 0   "must not exist yet". A create that races another
 *                        create fails loudly instead of one silently
 *                        clobbering the other.
 *   updating   cas = the version the user was looking at. The write lands only
 *                        if nobody has written since this panel loaded — which
 *                        is exactly the stale-tab case.
 *
 * The failure is surfaced as its own sentence rather than a raw error, because
 * "someone else changed this while you had the form open" is a thing a person
 * can act on and `cas` is not.
 */

import { useEffect, useId, useRef, useState } from "react";
import { AlertTriangle, EyeOff } from "lucide-react";
import { ConnectError, Code } from "@connectrpc/connect";

import Modal from "@/components/forge-ui/modal";
import { cn } from "@/lib/utils";

export interface SetSecretModalProps {
  open: boolean;
  onClose: () => void;
  /** The environment being written to. Shown because writing to the wrong env is easy and costly. */
  env: string;
  /**
   * The secret being updated, or null to create a new one. Drives cas: a name
   * means "update, from this version"; null means "create, must not exist".
   */
  existing: { name: string; currentVersion: number } | null;
  /** Names already in the store, so a create can refuse a collision before the round trip. */
  takenNames: string[];
  onSubmit: (args: { name: string; value: string; cas?: number }) => Promise<unknown>;
  isSubmitting: boolean;
  /** The mutation's error, if the last attempt failed. */
  error: Error | null;
}

/** forge's env-var naming shape. Matching it early beats a server rejection. */
const NAME_PATTERN = /^[A-Z][A-Z0-9_]*$/;

export function SetSecretModal({
  open,
  onClose,
  env,
  existing,
  takenNames,
  onSubmit,
  isSubmitting,
  error,
}: SetSecretModalProps) {
  const nameId = useId();
  const valueId = useId();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [touched, setTouched] = useState(false);
  const valueRef = useRef<HTMLInputElement>(null);

  const isUpdate = existing !== null;

  // Reset on every open. A modal that reopens holding the previous attempt's
  // value would keep a secret in component state indefinitely, and would also
  // pre-fill the wrong secret's value when the user opens a different row.
  useEffect(() => {
    if (!open) return;
    setName(existing?.name ?? "");
    setValue("");
    setTouched(false);
    // On an update the name is fixed, so the value is the only thing to type.
    if (isUpdate) valueRef.current?.focus();
  }, [open, existing, isUpdate]);

  // Clear the value from state on close, including a cancel. Without this the
  // typed secret would sit in React state until the component unmounted.
  useEffect(() => {
    if (open) return;
    setValue("");
  }, [open]);

  const trimmedName = name.trim();
  const nameCollision =
    !isUpdate && trimmedName !== "" && takenNames.includes(trimmedName);
  const nameMalformed = !isUpdate && trimmedName !== "" && !NAME_PATTERN.test(trimmedName);
  const nameError = nameCollision
    ? "A secret with this name already exists in this environment. Choose another name, or set a new version on the existing one."
    : nameMalformed
      ? "Use upper-case letters, digits and underscores, starting with a letter — the same shape as the environment variable it becomes."
      : null;

  const canSubmit =
    trimmedName !== "" && value !== "" && !nameError && !isSubmitting;

  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (!canSubmit) return;
    await onSubmit({
      name: trimmedName,
      value,
      // See the header: 0 asserts "must not exist", a version asserts "nobody
      // has written since I loaded this".
      cas: isUpdate ? existing.currentVersion : 0,
    });
    // The value is cleared by the close effect. Nothing echoes it back.
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="md"
      title={isUpdate ? `Set a new version of ${existing.name}` : "Add a secret"}
    >
      <form onSubmit={handleSubmit} className="space-y-5" data-testid="set-secret-form">
        {/*
         * The environment, stated as an identifier. Writing a production value
         * into dev (or the reverse) is the expensive mistake this form can
         * cause, and the only defence is saying which one is selected.
         */}
        <p className="text-sm text-muted-foreground">
          Writing to{" "}
          <span className="font-mono text-foreground">{env}</span>
          {isUpdate ? (
            <>
              {" "}
              — this creates version{" "}
              <span className="font-mono text-foreground">{existing.currentVersion + 1}</span> and
              leaves the previous versions in history.
            </>
          ) : (
            "."
          )}
        </p>

        <div className="space-y-2">
          <label htmlFor={nameId} className="block text-sm font-medium text-foreground">
            Name
          </label>
          <input
            id={nameId}
            value={name}
            onChange={(e) => setName(e.target.value)}
            onBlur={() => setTouched(true)}
            disabled={isUpdate}
            autoComplete="off"
            spellCheck={false}
            placeholder="DATABASE_URL"
            aria-invalid={!!nameError}
            aria-describedby={nameError ? `${nameId}-error` : undefined}
            /* Mono: this is an identifier, and it becomes an env var name. */
            className={cn(
              "w-full rounded-md border bg-background px-3 py-2 font-mono text-sm text-foreground",
              "placeholder:text-muted-foreground/60",
              "focus:outline-none focus:ring-2 focus:ring-primary/40",
              "disabled:cursor-not-allowed disabled:text-muted-foreground",
              nameError ? "border-destructive/60" : "border-border"
            )}
          />
          {isUpdate ? (
            <p className="text-xs text-muted-foreground">
              A secret&apos;s name is its identity in the store and cannot be changed.
            </p>
          ) : nameError && touched ? (
            <p id={`${nameId}-error`} className="text-xs text-destructive">
              {nameError}
            </p>
          ) : (
            <p className="text-xs text-muted-foreground">
              This is the name a workload references, and the environment variable it becomes.
            </p>
          )}
        </div>

        <div className="space-y-2">
          <label htmlFor={valueId} className="block text-sm font-medium text-foreground">
            Value
          </label>
          <input
            ref={valueRef}
            id={valueId}
            type="password"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            /* See the header: keeps it out of the browser's password manager. */
            autoComplete="new-password"
            spellCheck={false}
            aria-describedby={`${valueId}-writeonly`}
            className={cn(
              "w-full rounded-md border border-border bg-background px-3 py-2 font-mono text-sm text-foreground",
              "focus:outline-none focus:ring-2 focus:ring-primary/40"
            )}
          />

          {/*
           * The write-only explanation. An INSET on the modal surface —
           * bg-background + border-border/60 per the elevation rules, never
           * bg-muted, which inverts direction between light and dark.
           *
           * One calm paragraph attached to the field it describes, rather than
           * a banner at the top of the page.
           */}
          <div
            id={`${valueId}-writeonly`}
            data-testid="write-only-notice"
            className="flex gap-2.5 rounded-md border border-border/60 bg-background px-3 py-2.5"
          >
            <EyeOff
              className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground"
              aria-hidden="true"
            />
            <p className="text-xs leading-relaxed text-muted-foreground">
              <span className="text-foreground">You will not be able to read this back.</span> The
              store hands out values only to the workloads that declare them at deploy time — the
              credential reliant holds can write a version and list its history, but the store
              refuses it read access to the contents. There is no reveal to enable. Keep your own
              copy if you need one.
            </p>
          </div>
        </div>

        {error && <SubmitError error={error} isUpdate={isUpdate} />}

        <div className="flex justify-end gap-2 border-t border-border pt-4">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md px-3 py-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={!canSubmit}
            data-testid="set-secret-submit"
            className={cn(
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
              "bg-primary text-primary-foreground hover:bg-primary/90",
              "disabled:cursor-not-allowed disabled:opacity-50"
            )}
          >
            {isSubmitting ? "Saving…" : isUpdate ? "Save new version" : "Add secret"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/**
 * The failure sentence.
 *
 * A cas rejection gets its own wording because it is not really an error — it
 * is a concurrent edit, and the useful thing to say is what happened to the
 * user's copy of the truth, not which precondition the server evaluated.
 *
 * Note what is NOT rendered: the submitted value. `error.message` comes from
 * the transport and never carried one, and nothing here interpolates the form
 * state into a message.
 */
function SubmitError({ error, isUpdate }: { error: Error; isUpdate: boolean }) {
  const isCas =
    error instanceof ConnectError &&
    (error.code === Code.AlreadyExists || error.code === Code.FailedPrecondition);

  return (
    <div
      role="alert"
      data-testid="set-secret-error"
      className="flex gap-2.5 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2.5"
    >
      <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive" aria-hidden="true" />
      <p className="text-xs leading-relaxed text-destructive">
        {isCas
          ? isUpdate
            ? "Someone else wrote a new version of this secret while this form was open, so nothing was saved. Close this, re-read the current version, and try again."
            : "A secret with this name was created while this form was open, so nothing was saved. Set a new version on it instead."
          : `The secret could not be saved: ${error.message}`}
      </p>
    </div>
  );
}
