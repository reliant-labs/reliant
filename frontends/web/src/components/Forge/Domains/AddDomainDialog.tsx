// Copyright (c) 2025 Reliant Labs

/**
 * Add a domain and say what it serves, in ONE step.
 *
 * The control plane models these as two resources for good reasons — a
 * domain outlives any deployment, and a binding is one write that can move it
 * between environments — but that is a statement about LIFETIMES, not about
 * how the work arrives. Someone adding `example.com` already knows it is
 * for their web workload. Splitting it into "add, then come back and bind"
 * would leave the common case half-finished on screen, in the exact state
 * (claimed, unbound) that serves nothing.
 *
 * So the target is part of this form, and it is OPTIONAL: a domain being
 * parked, or one whose target has not been deployed yet, is a real case and
 * the server accepts an unbound domain.
 *
 * ── BIND BEFORE DNS IS DELIBERATE ───────────────────────────────────────────
 *
 * The domain will sit in PENDING_DNS for as long as propagation takes, and it
 * can be bound the whole time — `CreateDomainBinding` is documented as
 * bindable in any state, serving only when live. That is what lets a tenant
 * wire the whole thing up in one sitting and have it start serving by itself.
 *
 * ── THE TARGET IS PICKED, NEVER TYPED ───────────────────────────────────────
 *
 * The options are exactly what the environment runs that can answer HTTP —
 * services with an exposed port and static sites (domainTargetsOf). A typed
 * name is a typo the server cannot catch until the domain is live and
 * dialing nothing, so an environment with nothing to serve gets an empty
 * state that says what to deploy, not a free-text box. Parking the domain and
 * redirecting it stay available, because neither needs a target.
 *
 * ── REDIRECT IS A TARGET KIND, NOT A SEPARATE FLOW ──────────────────────────
 *
 * `www.example.com` → `example.com` is the single most common second
 * domain anyone adds. Making it a mode of the same picker rather than a
 * different dialog keeps it one decision ("what should this serve?") instead
 * of a fork the user has to find.
 *
 * ── THE SAME DIALOG RE-BINDS ────────────────────────────────────────────────
 *
 * Given `hostname`, the name is fixed and shown rather than asked for, the
 * current binding is pre-selected, and "nothing yet" is not offered — taking
 * a domain off the air is the detail view's "Stop serving", a separate and
 * deliberate action.
 */

import { useId, useMemo, useState } from "react";
import { AlertTriangle } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import { cn } from "@/lib/utils";
import { DOMAIN_TARGET_KIND_LABELS, type DomainTarget } from "@/services/forge/domains";

/** One environment the domain can be bound into, with the targets inside it. */
export interface DomainTargetEnv {
  /** The environment's name, as the user knows it ("prod"). */
  name: string;
  /** The control plane's id — what a binding is actually written against. */
  environmentId: string;
  /** What in this environment can serve a domain: exposed services and static sites. */
  targets: DomainTarget[];
  /** The target list has not been read yet — distinct from "this env serves nothing". */
  targetsLoading?: boolean;
}

/** A binding as the dialog pre-selects it when re-binding. */
export interface DomainBindingDraft {
  environmentId: string;
  target?: string;
  redirectTo?: string;
}

export interface AddDomainDialogProps {
  open: boolean;
  onClose: () => void;
  /** Environments with a control-plane id. An env without one cannot be bound. */
  envs: DomainTargetEnv[];
  /** Hostnames the org already holds, so a duplicate is refused before the round trip. */
  takenHostnames: string[];
  /** Re-binding an existing domain: the hostname is fixed and not asked for. */
  hostname?: string;
  /** The domain's current binding, pre-selected when re-binding. */
  current?: DomainBindingDraft | null;
  onSubmit: (args: {
    hostname: string;
    environmentId?: string;
    target?: string;
    redirectTo?: string;
  }) => Promise<unknown>;
  isSubmitting: boolean;
  error: Error | null;
}

/**
 * A hostname, loosely. Deliberately permissive — the server lowercases,
 * trims a trailing dot and applies the real rule, and a client-side regex
 * that is stricter than the server's is a way to refuse a name that would
 * actually have worked. This catches the typo class only: no dot, spaces, a
 * scheme or a path pasted in from a browser bar.
 */
const HOSTNAME_PATTERN = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i;

type TargetMode = "workload" | "redirect" | "none";

export function AddDomainDialog({
  open,
  onClose,
  envs,
  takenHostnames,
  hostname: fixedHostname,
  current,
  onSubmit,
  isSubmitting,
  error,
}: AddDomainDialogProps) {
  const hostnameId = useId();
  const envId = useId();
  const targetId = useId();
  const redirectId = useId();

  const rebinding = fixedHostname !== undefined;

  const [hostname, setHostname] = useState("");
  const [mode, setMode] = useState<TargetMode>(current?.redirectTo ? "redirect" : "workload");
  const [environmentId, setEnvironmentId] = useState(current?.environmentId ?? "");
  const [target, setTarget] = useState(current?.target ?? "");
  const [redirectTo, setRedirectTo] = useState(current?.redirectTo ?? "");
  const [touched, setTouched] = useState(false);

  // Reset each time the dialog OPENS, so a second use never inherits the
  // first's answers — a stale hostname here would be claimed against the
  // wrong name. Keyed on the open transition alone, not on `envs`: the
  // target lists are re-read while the dialog is up, and a reset on every
  // refetch would wipe a half-filled form.
  const [openedFor, setOpenedFor] = useState(open);
  if (open !== openedFor) {
    setOpenedFor(open);
    if (open) {
      setHostname("");
      setMode(current?.redirectTo ? "redirect" : "workload");
      setEnvironmentId(current?.environmentId ?? "");
      setTarget(current?.target ?? "");
      setRedirectTo(current?.redirectTo ?? "");
      setTouched(false);
    }
  }

  // Derived rather than stored, so environments that arrive after the dialog
  // opened are picked up without a reset.
  const selectedEnv = useMemo(
    () => envs.find((env) => env.environmentId === environmentId) ?? envs[0] ?? null,
    [envs, environmentId]
  );
  const effectiveEnvId = selectedEnv?.environmentId ?? "";
  const effectiveMode: TargetMode = envs.length === 0 && !rebinding ? "none" : mode;
  const targets = selectedEnv?.targets ?? [];
  const targetValid = targets.some((option) => option.name === target);

  const normalized = (fixedHostname ?? hostname).trim().toLowerCase().replace(/\.$/, "");
  const duplicate = !rebinding && takenHostnames.includes(normalized);
  const malformed = normalized.length > 0 && !HOSTNAME_PATTERN.test(normalized);
  const selfRedirect =
    effectiveMode === "redirect" && redirectTo.trim().toLowerCase() === normalized;

  const hostnameProblem = duplicate
    ? "Your organization already holds this domain."
    : malformed
      ? "That does not look like a hostname. Use the name on its own, with no scheme or path — for example app.example.com."
      : selfRedirect
        ? "A domain cannot redirect to itself."
        : null;

  const bindReady =
    (effectiveMode === "none" && !rebinding) ||
    (effectiveMode === "workload" && !!effectiveEnvId && targetValid) ||
    (effectiveMode === "redirect" && !!effectiveEnvId && !!redirectTo.trim());

  const canSubmit = !!normalized && !hostnameProblem && bindReady && !isSubmitting;

  const submit = () => {
    setTouched(true);
    if (!canSubmit) return;
    void onSubmit({
      hostname: normalized,
      environmentId: effectiveMode === "none" ? undefined : effectiveEnvId,
      target: effectiveMode === "workload" ? target : undefined,
      redirectTo: effectiveMode === "redirect" ? redirectTo.trim().toLowerCase() : undefined,
    });
  };

  const fieldClass =
    "w-full rounded-lg border border-border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring";

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={rebinding ? `Change what ${fixedHostname} serves` : "Add a custom domain"}
      description={
        rebinding
          ? "Point the domain at another target or environment. Its verification and certificate carry over, so there is no DNS to redo."
          : "Claim a hostname for your organization and choose what it should serve. You will get the DNS records to publish next."
      }
      size="lg"
      footer={
        <div className="flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-lg border border-border px-3 py-1.5 text-sm text-foreground transition hover:bg-muted"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={!canSubmit}
            className="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {rebinding
              ? isSubmitting
                ? "Saving…"
                : "Save target"
              : isSubmitting
                ? "Adding…"
                : "Add domain"}
          </button>
        </div>
      }
    >
      <form
        className="flex flex-col gap-5"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        {!rebinding && (
          <div className="flex flex-col gap-1.5">
            <label htmlFor={hostnameId} className="text-sm font-medium text-foreground">
              Hostname
            </label>
            <input
              id={hostnameId}
              value={hostname}
              onChange={(event) => setHostname(event.target.value)}
              onBlur={() => setTouched(true)}
              placeholder="app.example.com"
              autoComplete="off"
              spellCheck={false}
              className={cn(fieldClass, touched && hostnameProblem && "border-destructive")}
            />
            {touched && hostnameProblem ? (
              <p className="text-xs text-destructive-ink">{hostnameProblem}</p>
            ) : (
              <p className="text-xs text-muted-foreground">
                An apex (example.com) and its www are two separate domains. Add both, and bind the
                www one as a redirect.
              </p>
            )}
          </div>
        )}

        <fieldset className="flex flex-col gap-3">
          <legend className="text-sm font-medium text-foreground">What should it serve?</legend>
          <div className="flex flex-wrap gap-4 text-sm">
            <ModeRadio
              checked={effectiveMode === "workload"}
              onChange={() => setMode("workload")}
              disabled={envs.length === 0}
              label="A workload or site"
            />
            <ModeRadio
              checked={effectiveMode === "redirect"}
              onChange={() => setMode("redirect")}
              disabled={envs.length === 0}
              label="Redirect to another domain"
            />
            {!rebinding && (
              <ModeRadio
                checked={effectiveMode === "none"}
                onChange={() => setMode("none")}
                label="Nothing yet"
              />
            )}
          </div>

          {envs.length === 0 && (
            <p className="text-xs text-muted-foreground">
              None of this project&apos;s environments are run by Reliant cloud, so there is nothing
              to bind to yet.
              {!rebinding && " You can still claim the domain and bind it after your first deploy."}
            </p>
          )}

          {effectiveMode !== "none" && envs.length > 0 && (
            <div className="flex flex-col gap-3 rounded-lg border border-border/60 bg-background p-3">
              <div className="flex flex-col gap-1.5">
                <label htmlFor={envId} className="text-xs font-medium text-foreground">
                  Environment
                </label>
                <select
                  id={envId}
                  value={effectiveEnvId}
                  onChange={(event) => {
                    setEnvironmentId(event.target.value);
                    setTarget("");
                  }}
                  className={fieldClass}
                >
                  {envs.map((env) => (
                    <option key={env.environmentId} value={env.environmentId}>
                      {env.name}
                    </option>
                  ))}
                </select>
              </div>

              {effectiveMode === "workload" ? (
                <TargetPicker
                  id={targetId}
                  env={selectedEnv}
                  value={target}
                  onChange={setTarget}
                  canPark={!rebinding}
                  className={fieldClass}
                />
              ) : (
                <div className="flex flex-col gap-1.5">
                  <label htmlFor={redirectId} className="text-xs font-medium text-foreground">
                    Redirect to
                  </label>
                  <input
                    id={redirectId}
                    value={redirectTo}
                    onChange={(event) => setRedirectTo(event.target.value)}
                    placeholder="example.com"
                    autoComplete="off"
                    spellCheck={false}
                    className={fieldClass}
                  />
                  <p className="text-2xs text-muted-foreground">
                    Answers a 308 to this hostname, keeping the path and query. It never dials a
                    backend, so it keeps redirecting even while the app behind the other domain is
                    down.
                  </p>
                </div>
              )}
            </div>
          )}
        </fieldset>

        {error && (
          <div
            className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 p-3 text-xs text-destructive-ink"
            role="alert"
          >
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span>{error.message}</span>
          </div>
        )}
      </form>
    </Modal>
  );
}

/**
 * The target select, or — for an environment with nothing that serves HTTP —
 * an empty state that says what would fix it. Never a text box: see the
 * header comment.
 */
function TargetPicker({
  id,
  env,
  value,
  onChange,
  canPark,
  className,
}: {
  id: string;
  env: DomainTargetEnv | null;
  value: string;
  onChange: (target: string) => void;
  canPark: boolean;
  className: string;
}) {
  const targets = env?.targets ?? [];

  if (targets.length === 0) {
    return (
      <div className="flex flex-col gap-1.5">
        <span className="text-xs font-medium text-foreground">Target</span>
        {env?.targetsLoading ? (
          <p className="text-xs text-muted-foreground" data-testid="domain-targets-loading">
            Reading what {env.name} runs…
          </p>
        ) : (
          <div
            className="rounded-lg border border-dashed border-border px-3 py-2.5"
            data-testid="domain-targets-empty"
          >
            <p className="text-xs text-foreground">
              Nothing in {env?.name ?? "this environment"} serves HTTP yet.
            </p>
            <p className="mt-1 text-2xs text-muted-foreground">
              Deploy a workload with an exposed port or a static site, and it will be listed here.
              {canPark
                ? " Until then you can redirect this domain, or choose “Nothing yet” and bind it later."
                : " Until then you can redirect this domain instead."}
            </p>
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-xs font-medium text-foreground">
        Target
      </label>
      <select
        id={id}
        value={targets.some((option) => option.name === value) ? value : ""}
        onChange={(event) => onChange(event.target.value)}
        className={className}
      >
        <option value="">Choose a service or static site…</option>
        {targets.map((option) => (
          <option key={option.name} value={option.name}>
            {option.name} — {DOMAIN_TARGET_KIND_LABELS[option.kind]}
          </option>
        ))}
      </select>
      <p className="text-2xs text-muted-foreground">
        Services with an exposed port and static sites deployed to {env?.name}. A domain cannot
        point at a worker, a job or a database — none of them answer HTTP.
      </p>
    </div>
  );
}

function ModeRadio({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: () => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <label
      className={cn(
        "inline-flex items-center gap-2",
        disabled ? "cursor-not-allowed text-muted-foreground" : "cursor-pointer text-foreground"
      )}
    >
      <input
        type="radio"
        name="domain-target-mode"
        checked={checked}
        onChange={onChange}
        disabled={disabled}
        className="accent-primary"
      />
      {label}
    </label>
  );
}
