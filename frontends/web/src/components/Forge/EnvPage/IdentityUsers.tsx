// Copyright (c) 2025 Reliant Labs

/**
 * The Users and Invite panels of the Identity tab: who can sign in to this
 * environment, in what state, with which roles — and, for an org admin, the
 * verbs that change that.
 *
 * ROLES COME IN TWO KINDS. A role the environment declares with
 * `members_from = declared` is converged from KCL on every reconcile; editing
 * it here would be undone, so the server refuses and this screen shows it
 * read-only, saying where it is managed. Every other declared role is
 * console-managed: granted and revoked here. SetUserRoles replaces the
 * COMPLETE console-managed set, so the editor sends the whole set it shows.
 *
 * DESTRUCTIVE VERBS ASK FIRST. Delete, lock and revoke-sessions each open a
 * confirmation that says what happens to the person; a password reset only
 * sends an email and unlock only gives access back, so they run at once.
 *
 * EMAIL OR A LINK. With smtp declared, invite and reset email the person; an
 * admin can also take a one-time link instead ("Get reset link", or the
 * invite's "give me the link" box). With no mail delivery, emailing is not
 * offered at all — the server would refuse it — and both verbs hand back the
 * link. The link is a credential and is only ever shown in
 * IdentityPasswordCodeDialog.
 */

import { useMemo, useState } from "react";
import { Link2, Lock, MailPlus, Search, ShieldCheck, Users } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import Button from "@/components/forge-ui/button";
import { useIdentityMutation, useIdentityUsers } from "@/hooks/identity-queries";
import type {
  IdentityRole,
  IdentityStatus,
  IdentityUser,
  IdentityUserFilter,
  IdentityUserState,
} from "@/services/controlPlane/identity/api";
import { identityErrorKind, identityErrorMessage } from "@/services/controlPlane/identity/errors";
import { toast } from "@/lib/toast-manager";
import { cn } from "@/lib/utils";

import { formatAgo } from "../Observability/format";
import { Panel } from "./EnvTabPanels";
import { IdentityPasswordCodeDialog, type IssuedCode } from "./IdentityPasswordCodeDialog";

export const DECLARED_ROLE_NOTE = "managed in deploy/kcl/identity.k";

const USER_STATE_LABEL: Record<IdentityUserState, string> = {
  invited: "Invited",
  active: "Active",
  locked: "Locked",
  disabled: "Disabled",
  unknown: "Unknown",
};

const USER_STATE_VARIANT: Record<IdentityUserState, "success" | "warning" | "error" | "info" | "neutral"> = {
  invited: "info",
  active: "success",
  locked: "warning",
  disabled: "neutral",
  unknown: "neutral",
};

const STATE_FILTERS: { value: IdentityUserFilter["state"]; label: string }[] = [
  { value: undefined, label: "All" },
  { value: "active", label: "Active" },
  { value: "invited", label: "Invited" },
  { value: "locked", label: "Locked" },
  { value: "disabled", label: "Disabled" },
];

type Confirm = { kind: "delete" | "lock" | "revoke"; user: IdentityUser };

export function IdentityUsers({
  orgId,
  envId,
  envName,
  status,
}: {
  orgId: string;
  envId: string;
  envName: string;
  status: IdentityStatus;
}) {
  const canManage = status.callerCanManage;
  // User verbs open the instance with its owner key, which only a ready (or
  // degraded, still serving) instance has.
  const serving = status.state === "ready" || status.state === "degraded";
  // "unknown" (a server that predates the field) keeps offering email: it is
  // what that server does.
  const canEmail = status.mailDelivery !== "none";

  const [query, setQuery] = useState("");
  const [state, setState] = useState<IdentityUserFilter["state"]>(undefined);
  const filter = useMemo(() => ({ query, state }), [query, state]);
  const users = useIdentityUsers(orgId, envId, filter, serving);

  const [editing, setEditing] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<Confirm | null>(null);

  const roles: IdentityRole[] = users.data?.pages[0]?.roles ?? [];
  const rows = users.data?.pages.flatMap((page) => page.users) ?? [];
  const total = users.data?.pages[0]?.totalSize ?? null;
  const reportsSignIns = users.data?.pages[0]?.reportsSignIns ?? false;

  const done = (message: string) => () => toast.success(message);
  const failed = (what: string) => (error: unknown) =>
    toast.warning(`${what}: ${identityErrorMessage(error) || "the server refused it"}`);

  // The only copy of a returned code: dropped when the dialog closes.
  const [issued, setIssued] = useState<IssuedCode | null>(null);

  const reset = useIdentityMutation(orgId, envId, (api, o, e, u: IdentityUser) => api.sendPasswordReset(o, e, u.userId));
  const resetLink = useIdentityMutation(
    orgId,
    envId,
    (api, o, e, u: IdentityUser) => api.sendPasswordReset(o, e, u.userId, { returnCode: true }),
    { secret: true }
  );
  const getResetLink = (user: IdentityUser) =>
    resetLink.mutate(user, {
      onSuccess: (code) =>
        setIssued(
          code
            ? { email: user.email, purpose: "reset", code }
            : { email: user.email, purpose: "reset", error: new Error("The server returned no code.") }
        ),
      onError: (error) => setIssued({ email: user.email, purpose: "reset", error }),
    });
  const closeIssued = () => {
    setIssued(null);
    resetLink.reset();
  };
  const lock = useIdentityMutation(orgId, envId, (api, o, e, u: IdentityUser) => api.lockUser(o, e, u.userId));
  const unlock = useIdentityMutation(orgId, envId, (api, o, e, u: IdentityUser) => api.unlockUser(o, e, u.userId));
  const remove = useIdentityMutation(orgId, envId, (api, o, e, u: IdentityUser) => api.deleteUser(o, e, u.userId));
  const revoke = useIdentityMutation(orgId, envId, (api, o, e, u: IdentityUser) => api.revokeSessions(o, e, u.userId));

  const confirming = confirm?.kind === "delete" ? remove : confirm?.kind === "lock" ? lock : revoke;
  const runConfirmed = () => {
    if (!confirm) return;
    const { kind, user } = confirm;
    const close = () => setConfirm(null);
    if (kind === "delete") {
      remove.mutate(user, { onSuccess: done(`${user.email} was deleted`), onError: failed("Couldn't delete"), onSettled: close });
    } else if (kind === "lock") {
      lock.mutate(user, { onSuccess: done(`${user.email} is locked`), onError: failed("Couldn't lock"), onSettled: close });
    } else {
      revoke.mutate(user, {
        onSuccess: (n) => toast.success(`Ended ${n} session${n === 1 ? "" : "s"} for ${user.email}`),
        onError: failed("Couldn't end sessions"),
        onSettled: close,
      });
    }
  };

  let body;
  if (!serving) {
    body = (
      <p className="px-4 py-5 text-xs text-muted-foreground" data-testid="identity-users-not-serving">
        Users can be listed and managed once the sign-in service is ready.
      </p>
    );
  } else if (users.error) {
    const kind = identityErrorKind(users.error);
    body = (
      <div className="space-y-2 px-4 py-5 text-xs" role="alert" data-testid="identity-users-error">
        <p className="text-foreground">
          {kind === "not-ready" ? "The sign-in service isn't ready for user administration yet." : "Couldn't list users."}
        </p>
        {identityErrorMessage(users.error) && <p className="font-mono text-muted-foreground">{identityErrorMessage(users.error)}</p>}
        <Button variant="outline" size="sm" onClick={() => void users.refetch()} loading={users.isFetching}>
          Try again
        </Button>
      </div>
    );
  } else if (users.isLoading) {
    body = (
      <div className="space-y-2 px-4 py-4" data-testid="identity-users-loading" aria-busy="true">
        {[0, 1, 2].map((i) => (
          <div key={i} className="h-9 animate-pulse rounded-md border border-border/60 bg-background motion-reduce:animate-none" />
        ))}
      </div>
    );
  } else if (rows.length === 0) {
    body = (
      <div className="flex items-start gap-3 px-4 py-5 text-xs text-muted-foreground" data-testid="identity-users-empty">
        <Users className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
        <p>
          {query || state
            ? "Nobody matches this search."
            : canManage
              ? `Nobody can sign in to ${envName} yet. Invite someone below.`
              : `Nobody can sign in to ${envName} yet.`}
        </p>
      </div>
    );
  } else {
    body = (
      <ul className="divide-y divide-border" data-testid="identity-users">
        {rows.map((user) => (
          <UserRow
            key={user.userId}
            user={user}
            roles={roles}
            canManage={canManage}
            editing={editing === user.userId}
            onEdit={() => setEditing(editing === user.userId ? null : user.userId)}
            orgId={orgId}
            envId={envId}
            reportsSignIns={reportsSignIns}
            busy={reset.isPending || resetLink.isPending || unlock.isPending}
            canEmail={canEmail}
            onReset={() =>
              reset.mutate(user, { onSuccess: done(`Emailed ${user.email} a password reset link`), onError: failed("Couldn't send the reset") })
            }
            onResetLink={() => getResetLink(user)}
            onUnlock={() => unlock.mutate(user, { onSuccess: done(`${user.email} is unlocked`), onError: failed("Couldn't unlock") })}
            onConfirm={(kind) => setConfirm({ kind, user })}
          />
        ))}
      </ul>
    );
  }

  return (
    <>
      <Panel
        title="Users"
        description="Everyone who can sign in to this environment's applications."
        actions={
          serving &&
          total !== null && (
            <span className="text-xs text-muted-foreground" data-testid="identity-users-total">
              {rows.length < total ? `${rows.length} of ${total}` : total} {total === 1 ? "user" : "users"}
            </span>
          )
        }
        testId="identity-users-panel"
        flush
      >
        {serving && (
          <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-3">
            <label className="relative min-w-[12rem] flex-1">
              <span className="sr-only">Search users by email</span>
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
              <input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search by email"
                maxLength={200}
                data-testid="identity-users-search"
                className="w-full rounded-md border border-input bg-background py-1.5 pl-8 pr-2 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
              />
            </label>
            <div role="group" aria-label="Filter by state" className="inline-flex rounded-lg border border-border bg-background p-0.5">
              {STATE_FILTERS.map((option) => (
                <button
                  key={option.label}
                  type="button"
                  aria-pressed={state === option.value}
                  onClick={() => setState(option.value)}
                  className={cn(
                    "rounded-md px-2.5 py-1 text-xs transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    state === option.value ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"
                  )}
                >
                  {option.label}
                </button>
              ))}
            </div>
          </div>
        )}
        {body}
        {users.hasNextPage && (
          <div className="border-t border-border px-4 py-2">
            <Button variant="ghost" size="sm" onClick={() => void users.fetchNextPage()} loading={users.isFetchingNextPage}>
              Show more
            </Button>
          </div>
        )}
      </Panel>

      {canManage && serving && (
        <InvitePanel orgId={orgId} envId={envId} envName={envName} roles={roles} canEmail={canEmail} onIssued={setIssued} />
      )}

      <IdentityPasswordCodeDialog issued={issued} onClose={closeIssued} />

      <ConfirmationDialog
        open={!!confirm}
        title={confirm ? CONFIRM_COPY[confirm.kind].title(confirm.user.email) : ""}
        description={confirm ? CONFIRM_COPY[confirm.kind].description : undefined}
        confirmLabel={confirm ? CONFIRM_COPY[confirm.kind].confirm : undefined}
        variant={confirm?.kind === "delete" ? "danger" : "warning"}
        loading={confirming.isPending}
        onCancel={() => setConfirm(null)}
        onConfirm={runConfirmed}
      />
    </>
  );
}

const CONFIRM_COPY: Record<Confirm["kind"], { title: (email: string) => string; description: string; confirm: string }> = {
  delete: {
    title: (email) => `Delete ${email}?`,
    description:
      "Their account is removed from this environment's sign-in service, with every role they hold. Inviting them again creates a new account.",
    confirm: "Delete user",
  },
  lock: {
    title: (email) => `Lock ${email}?`,
    description:
      "They can't sign in until someone unlocks them. Sessions already open keep working until they end — revoke them too to sign them out now.",
    confirm: "Lock user",
  },
  revoke: {
    title: (email) => `End every session for ${email}?`,
    description:
      "They are signed out everywhere and must sign in again. Access tokens already issued stay valid until they expire, usually within the hour.",
    confirm: "End sessions",
  },
};

function UserRow({
  user,
  roles,
  canManage,
  editing,
  onEdit,
  orgId,
  envId,
  reportsSignIns,
  busy,
  canEmail,
  onReset,
  onResetLink,
  onUnlock,
  onConfirm,
}: {
  user: IdentityUser;
  roles: IdentityRole[];
  canManage: boolean;
  editing: boolean;
  onEdit: () => void;
  orgId: string;
  envId: string;
  /** Whether a missing last sign-in means "never" (the server reports them). */
  reportsSignIns: boolean;
  busy: boolean;
  canEmail: boolean;
  onReset: () => void;
  onResetLink: () => void;
  onUnlock: () => void;
  onConfirm: (kind: Confirm["kind"]) => void;
}) {
  const declared = new Set(roles.filter((r) => r.declared).map((r) => r.key));
  const holdsDeclared = user.roles.some((r) => declared.has(r));
  return (
    <li className="space-y-2 px-4 py-3" data-testid={`identity-user-${user.userId}`}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate text-sm text-foreground">{user.email}</span>
            <Badge label={USER_STATE_LABEL[user.state]} variant={USER_STATE_VARIANT[user.state]} size="sm" />
          </div>
          <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            {user.displayName && user.displayName !== user.email && <span>{user.displayName}</span>}
            {user.createdAtMs && <span>added {new Date(user.createdAtMs).toLocaleDateString()}</span>}
            {user.lastSignInAtMs ? (
              <span data-testid="identity-user-last-sign-in" title={new Date(user.lastSignInAtMs).toLocaleString()}>
                signed in {formatAgo(user.lastSignInAtMs)}
              </span>
            ) : (
              reportsSignIns && <span data-testid="identity-user-last-sign-in">never signed in</span>
            )}
            {user.roles.length === 0 ? (
              <span>no roles</span>
            ) : (
              user.roles.map((role) => (
                <span
                  key={role}
                  title={declared.has(role) ? DECLARED_ROLE_NOTE : undefined}
                  className="inline-flex items-center gap-1 rounded border border-border/60 bg-background px-1.5 py-0.5 font-mono text-2xs text-foreground"
                >
                  {declared.has(role) && <Lock className="h-2.5 w-2.5" aria-label="declared" />}
                  {role}
                </span>
              ))
            )}
          </div>
        </div>
        {canManage && (
          <div className="flex flex-wrap items-center gap-1" data-testid="identity-user-actions">
            <Button variant="ghost" size="xs" onClick={onEdit} aria-expanded={editing}>
              Roles
            </Button>
            {canEmail && (
              <Button variant="ghost" size="xs" onClick={onReset} disabled={busy} title="Emails them a link to set a new password">
                Email reset
              </Button>
            )}
            <Button
              variant="ghost"
              size="xs"
              onClick={onResetLink}
              disabled={busy}
              leftIcon={<Link2 className="h-3 w-3" />}
              title="A one-time link to set their password, for you to hand over"
            >
              Get reset link
            </Button>
            {user.state === "locked" ? (
              <Button variant="ghost" size="xs" onClick={onUnlock} disabled={busy}>
                Unlock
              </Button>
            ) : (
              <Button variant="ghost" size="xs" onClick={() => onConfirm("lock")}>
                Lock
              </Button>
            )}
            <Button variant="ghost" size="xs" onClick={() => onConfirm("revoke")}>
              End sessions
            </Button>
            <Button
              variant="ghost"
              size="xs"
              className="text-destructive-ink!"
              onClick={() => onConfirm("delete")}
              disabled={holdsDeclared}
              title={holdsDeclared ? `Holds a declared role (${DECLARED_ROLE_NOTE}); the next reconcile would recreate them` : undefined}
            >
              Delete
            </Button>
          </div>
        )}
      </div>
      {canManage && editing && <RoleEditor user={user} roles={roles} orgId={orgId} envId={envId} onDone={onEdit} />}
    </li>
  );
}

function RoleEditor({
  user,
  roles,
  orgId,
  envId,
  onDone,
}: {
  user: IdentityUser;
  roles: IdentityRole[];
  orgId: string;
  envId: string;
  onDone: () => void;
}) {
  const managed = roles.filter((r) => !r.declared);
  const [selected, setSelected] = useState<Set<string>>(
    () => new Set(user.roles.filter((r) => managed.some((m) => m.key === r)))
  );
  const save = useIdentityMutation(orgId, envId, (api, o, e, next: string[]) => api.setUserRoles(o, e, { userId: user.userId, roles: next }));
  return (
    <div className="space-y-3 rounded-md border border-border/60 bg-background p-3" data-testid="identity-role-editor">
      <RoleChecklist roles={roles} selected={selected} onChange={setSelected} heldDeclared={new Set(user.roles)} />
      {save.error && (
        <p role="alert" className="text-xs text-destructive-ink">
          {identityErrorMessage(save.error) || "Couldn't save the roles."}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onDone}>
          Cancel
        </Button>
        <Button
          size="sm"
          loading={save.isPending}
          disabled={managed.length === 0}
          onClick={() =>
            save.mutate([...selected], {
              onSuccess: () => {
                toast.success(`Updated ${user.email}'s roles`);
                onDone();
              },
            })
          }
        >
          Save roles
        </Button>
      </div>
    </div>
  );
}

/**
 * Console-managed roles as checkboxes; declared roles listed beside them,
 * disabled, with where they are managed. `heldDeclared` ticks the declared
 * roles the person already holds, so the reader sees their full set.
 */
function RoleChecklist({
  roles,
  selected,
  onChange,
  heldDeclared,
}: {
  roles: IdentityRole[];
  selected: Set<string>;
  onChange: (next: Set<string>) => void;
  heldDeclared: Set<string>;
}) {
  if (roles.length === 0) {
    return <p className="text-xs text-muted-foreground">This environment declares no roles.</p>;
  }
  const toggle = (key: string) => {
    const next = new Set(selected);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    onChange(next);
  };
  return (
    <fieldset className="space-y-1.5">
      <legend className="mb-1 text-xs font-medium text-foreground">Roles</legend>
      {roles.map((role) => (
        <label
          key={role.key}
          className={cn("flex items-start gap-2 text-xs", role.declared ? "text-muted-foreground" : "text-foreground")}
          data-testid={`identity-role-${role.key}`}
        >
          <input
            type="checkbox"
            className="mt-0.5"
            checked={role.declared ? heldDeclared.has(role.key) : selected.has(role.key)}
            disabled={role.declared}
            onChange={() => toggle(role.key)}
          />
          <span>
            <span className="font-mono">{role.key}</span>
            {role.displayName && role.displayName !== role.key && <span> — {role.displayName}</span>}
            {role.declared && (
              <span className="ml-1 inline-flex items-center gap-1" data-testid="identity-role-declared-note">
                <Lock className="h-2.5 w-2.5" aria-hidden="true" />
                managed in <code className="font-mono">deploy/kcl/identity.k</code>
              </span>
            )}
          </span>
        </label>
      ))}
    </fieldset>
  );
}

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

function InvitePanel({
  orgId,
  envId,
  envName,
  roles,
  canEmail,
  onIssued,
}: {
  orgId: string;
  envId: string;
  envName: string;
  roles: IdentityRole[];
  canEmail: boolean;
  onIssued: (issued: IssuedCode) => void;
}) {
  const [email, setEmail] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [touched, setTouched] = useState(false);
  // With no mail delivery the link is the only way; with smtp it is opt-in.
  const [wantLink, setWantLink] = useState(false);
  const returnCode = !canEmail || wantLink;
  const invite = useIdentityMutation(
    orgId,
    envId,
    (api, o, e, args: { email: string; roles: string[]; returnCode: boolean }) => api.inviteUser(o, e, args),
    { secret: true }
  );
  const valid = EMAIL.test(email.trim());
  return (
    <Panel
      title="Invite"
      description={
        canEmail
          ? `Emails them a link to set a password for ${envName}, or gives you that link to send yourself. An address that already has an account is given the roles instead.`
          : `${envName}'s identity sends no email, so nothing is emailed: you get a one-time link that lets them set a password, and send it to them yourself. An address that already has an account is given the roles instead.`
      }
      testId="identity-invite"
    >
      <form
        className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          setTouched(true);
          if (!valid) return;
          const to = email.trim();
          const clear = () => {
            setEmail("");
            setSelected(new Set());
            setTouched(false);
            invite.reset();
          };
          invite.mutate(
            { email: to, roles: [...selected], returnCode },
            {
              onSuccess: (r) => {
                clear();
                if (r.code) {
                  onIssued({ email: to, purpose: "invite", code: r.code });
                } else if (!r.created) {
                  toast.success(
                    returnCode
                      ? `${to} already had an account and a password; their roles were added. Use Get reset link if they need a new one.`
                      : `${to} already had an account; their roles were added`
                  );
                } else {
                  toast.success(returnCode ? `Created ${to}` : `Emailed ${to} an invitation`);
                }
              },
              onError: (error) => {
                // A refused code is explained in context, with what to do about it.
                if (returnCode && identityErrorKind(error) === "denied") onIssued({ email: to, purpose: "invite", error });
              },
            }
          );
        }}
      >
        <label className="block space-y-1">
          <span className="text-xs font-medium text-foreground">Email</span>
          <input
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            onBlur={() => setTouched(true)}
            maxLength={254}
            placeholder="person@example.com"
            aria-invalid={touched && !valid}
            data-testid="identity-invite-email"
            className="w-full max-w-md rounded-md border border-input bg-background px-2.5 py-1.5 text-sm text-foreground placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
          />
          {touched && !valid && email && <span className="text-xs text-destructive-ink">That doesn&apos;t look like an email address.</span>}
        </label>
        <RoleChecklist roles={roles} selected={selected} onChange={setSelected} heldDeclared={new Set()} />
        {canEmail && (
          <label className="flex items-start gap-2 text-xs text-foreground">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={wantLink}
              onChange={(event) => setWantLink(event.target.checked)}
              data-testid="identity-invite-want-link"
            />
            <span>Give me the link instead of emailing it</span>
          </label>
        )}
        {invite.error && !(returnCode && identityErrorKind(invite.error) === "denied") && (
          <p role="alert" className="text-xs text-destructive-ink">
            {identityErrorMessage(invite.error) || "Couldn't send the invitation."}
          </p>
        )}
        <div className="flex items-center gap-2">
          <Button
            type="submit"
            size="sm"
            loading={invite.isPending}
            disabled={!valid}
            leftIcon={returnCode ? <Link2 className="h-3.5 w-3.5" /> : <MailPlus className="h-3.5 w-3.5" />}
          >
            {returnCode ? "Invite and get link" : "Send invitation"}
          </Button>
          <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
            <ShieldCheck className="h-3 w-3" aria-hidden="true" />
            Recorded in your organization&apos;s audit log
          </span>
        </div>
      </form>
    </Panel>
  );
}
