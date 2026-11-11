// Copyright (c) 2025 Reliant Labs

/**
 * The Users and Invite panels of the Identity tab: who can sign in to this
 * environment, in what state, with which roles — and, for an org admin, the
 * verbs that change that.
 *
 * ONE ROW PER PERSON. The list is a table: who (email, name), state, roles,
 * when they were added and when they last signed in. Every verb lives in the
 * row's "⋯" menu, Delete last and apart; a click on the row opens the person
 * (UserDetail: everything about them, the same verbs as buttons, and the role
 * editor). On a phone each row is a card with the same menu.
 *
 * SORTING IS OF WHAT IS LOADED. The server lists people oldest first and
 * takes no sort, so "Last sign-in" and "Added" reorder the pages already
 * fetched; while more remain, the footer says so rather than implying the
 * whole population was ranked.
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

import { useContext, useMemo, useState } from "react";
import { ForgeConsoleContext } from "../Console/consoleContext";
import { useSearch, useNavigate } from "@tanstack/react-router";
import { IdentitySelection, useIdentityAppDataHref } from "./IdentitySelection";
import { Database, Link2, Lock, MailPlus, MoreHorizontal, Search, ShieldCheck, Users } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import DropdownMenu, { type MenuGroup } from "@/components/forge-ui/dropdown_menu";
import Link from "@/components/forge-ui/link";
import Modal from "@/components/forge-ui/modal";
import { useIdentityMutation, useIdentityUser, useIdentityUsers } from "@/hooks/identity-queries";
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

import { identityDataLinksEnabled, validIdentityId } from "../Data/identityLinks-model";
import { formatAgo } from "../Observability/format";
import { useObsReadOnly } from "../Observability/obsReadOnly";
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
type SortKey = "added" | "lastSignIn";
type UserRowData = { id: string; user: IdentityUser };

/** Which person is open, and whether their role editor is. */
type Opened = { userId: string | null; editing: boolean };

type IdentityUsersProps = { orgId: string; envId: string; envName: string; status: IdentityStatus };
export function IdentityUsers(props: IdentityUsersProps) {
  const console = useContext(ForgeConsoleContext);
  const [query, setQuery] = useState("");
  const [opened, setOpened] = useState<Opened>({ userId: null, editing: false });
  return console ? (
    <RoutedIdentityUsers {...props} />
  ) : (
    <IdentityUsersBody {...props} query={query} setQuery={setQuery} opened={opened} setOpened={setOpened} />
  );
}

/**
 * In the console the search and the open person are in the URL (`q`, `user`),
 * so "look at this person" is a link. A build with identity data links
 * contained keeps the open person in component state instead: it does no
 * lookup from a URL.
 */
function RoutedIdentityUsers(props: IdentityUsersProps) {
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const navigate = useNavigate();
  const routed = identityDataLinksEnabled();
  const [editing, setEditing] = useState(false);
  const [localUser, setLocalUser] = useState<string | null>(null);
  const query = typeof search.q === "string" && search.q.length <= 200 ? search.q : "";
  const urlUser = routed && validIdentityId(search.user) ? search.user : null;
  const setQuery = (q: string) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, q: q || undefined, user: undefined }), replace: true });
  const setOpened = (next: Opened) => {
    setEditing(next.editing);
    if (!routed) return setLocalUser(next.userId);
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, user: next.userId ?? undefined }) });
  };
  const serving = props.status.state === "ready" || props.status.state === "degraded";
  return (
    <>
      {!urlUser && <IdentitySelection orgId={props.orgId} envId={props.envId} serving={serving} />}
      <IdentityUsersBody
        {...props}
        query={query}
        setQuery={setQuery}
        opened={{ userId: routed ? urlUser : localUser, editing }}
        setOpened={setOpened}
      />
    </>
  );
}

function IdentityUsersBody({
  query,
  setQuery,
  opened,
  setOpened,
  orgId,
  envId,
  envName,
  status,
}: IdentityUsersProps & {
  query: string;
  setQuery: (query: string) => void;
  opened: Opened;
  setOpened: (opened: Opened) => void;
}) {
  const canManage = status.callerCanManage;
  // User verbs open the instance with its owner key, which only a ready (or
  // degraded, still serving) instance has.
  const serving = status.state === "ready" || status.state === "degraded";
  // "unknown" (a server that predates the field) keeps offering email: it is
  // what that server does.
  const canEmail = status.mailDelivery !== "none";
  // The demo console (and any read-only host) shows the verbs, disabled, with why.
  const { readOnly, reason } = useObsReadOnly();
  const lockedReason = readOnly ? (reason ?? "Read-only") : undefined;

  const [state, setState] = useState<IdentityUserFilter["state"]>(undefined);
  const filter = useMemo(() => ({ query, state }), [query, state]);
  const users = useIdentityUsers(orgId, envId, filter, serving);
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" } | null>(null);

  const [confirm, setConfirm] = useState<Confirm | null>(null);

  const roles: IdentityRole[] = users.data?.pages[0]?.roles ?? [];
  const loaded = useMemo(() => users.data?.pages.flatMap((page) => page.users) ?? [], [users.data]);
  const rows = useMemo<UserRowData[]>(() => {
    const list = loaded.map((user) => ({ id: user.userId, user }));
    if (!sort) return list;
    const value = (row: UserRowData) => (sort.key === "added" ? row.user.createdAtMs : row.user.lastSignInAtMs) ?? -Infinity;
    return [...list].sort((a, b) => (sort.dir === "asc" ? value(a) - value(b) : value(b) - value(a)));
  }, [loaded, sort]);
  const total = users.data?.pages[0]?.totalSize ?? null;
  const reportsSignIns = users.data?.pages[0]?.reportsSignIns ?? false;
  const declared = useMemo(() => new Set(roles.filter((r) => r.declared).map((r) => r.key)), [roles]);

  // The open person: from the loaded pages when they are on one, else read on its own (a link to someone further down).
  const openRow = opened.userId ? loaded.find((user) => user.userId === opened.userId) ?? null : null;
  const fetched = useIdentityUser(orgId, envId, opened.userId, serving && !!opened.userId && !openRow);
  const openUser = openRow ?? (fetched.data?.userId === opened.userId ? fetched.data : null);
  const open = (user: IdentityUser, editing = false) => setOpened({ userId: user.userId, editing });
  const close = () => setOpened({ userId: null, editing: false });

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
    const settle = () => setConfirm(null);
    if (kind === "delete") {
      remove.mutate(user, {
        onSuccess: () => {
          toast.success(`${user.email} was deleted`);
          if (opened.userId === user.userId) close();
        },
        onError: failed("Couldn't delete"),
        onSettled: settle,
      });
    } else if (kind === "lock") {
      lock.mutate(user, { onSuccess: done(`${user.email} is locked`), onError: failed("Couldn't lock"), onSettled: settle });
    } else {
      revoke.mutate(user, {
        onSuccess: (n) => toast.success(`Ended ${n} session${n === 1 ? "" : "s"} for ${user.email}`),
        onError: failed("Couldn't end sessions"),
        onSettled: settle,
      });
    }
  };

  const verbs: UserVerbs = {
    canManage,
    canEmail,
    lockedReason,
    busy: reset.isPending || resetLink.isPending || unlock.isPending,
    declared,
    open,
    reset: (user) => reset.mutate(user, { onSuccess: done(`Emailed ${user.email} a password reset link`), onError: failed("Couldn't send the reset") }),
    resetLink: getResetLink,
    unlock: (user) => unlock.mutate(user, { onSuccess: done(`${user.email} is unlocked`), onError: failed("Couldn't unlock") }),
    confirm: (kind, user) => setConfirm({ kind, user }),
  };

  const columns: DataTableColumn<UserRowData>[] = [
    {
      key: "user",
      header: "User",
      stackTitle: true,
      // Two lines (20px + 16px) in 4px of padding: a 44px row. On a phone it leaves room for the card's menu.
      className: "py-1 max-md:pr-10!",
      render: (_, { user }) => (
        <span className="flex min-w-0 flex-col">
          <span className="truncate text-sm leading-5 text-ink">{user.email}</span>
          {user.displayName && user.displayName !== user.email && <span className="truncate text-xs leading-4 text-ink-muted">{user.displayName}</span>}
        </span>
      ),
    },
    {
      key: "state",
      header: "State",
      width: "6.5rem",
      render: (_, { user }) => <Badge label={USER_STATE_LABEL[user.state]} variant={USER_STATE_VARIANT[user.state]} size="sm" />,
    },
    {
      key: "roles",
      header: "Roles",
      width: "9.5rem",
      render: (_, { user }) => <RoleTags roles={user.roles} declared={declared} max={2} />,
    },
    {
      key: "added",
      header: "Added",
      width: "7rem",
      sortable: true,
      className: "text-xs text-ink-muted",
      render: (_, { user }) =>
        user.createdAtMs ? <time dateTime={new Date(user.createdAtMs).toISOString()} title={new Date(user.createdAtMs).toLocaleString()}>{shortDate(user.createdAtMs)}</time> : "—",
    },
    {
      key: "lastSignIn",
      header: "Last sign-in",
      width: "7.5rem",
      sortable: true,
      className: "text-xs text-ink-muted",
      render: (_, { user }) => <LastSignIn user={user} reportsSignIns={reportsSignIns} />,
    },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      interactive: true,
      width: "3rem",
      // On a phone, the card's top-right corner rather than a labelled "Actions" line.
      className: "text-right max-md:absolute max-md:right-3 max-md:top-2 max-md:w-auto! max-md:before:hidden",
      render: (_, { user }) => <UserMenu user={user} envId={envId} verbs={verbs} />,
    },
  ];

  let body;
  if (!serving) {
    body = (
      <p className="px-4 py-5 text-xs text-ink-muted" data-testid="identity-users-not-serving">
        Users can be listed and managed once the sign-in service is ready.
      </p>
    );
  } else if (users.error) {
    const kind = identityErrorKind(users.error);
    body = (
      <div className="space-y-2 px-4 py-5 text-xs" role="alert" data-testid="identity-users-error">
        <p className="text-ink">
          {kind === "not-ready" ? "The sign-in service isn't ready for user administration yet." : "Couldn't list users."}
        </p>
        {identityErrorMessage(users.error) && <p className="font-mono text-ink-muted">{identityErrorMessage(users.error)}</p>}
        <Button variant="outline" size="sm" onClick={() => void users.refetch()} loading={users.isFetching}>
          Try again
        </Button>
      </div>
    );
  } else if (users.isLoading) {
    body = (
      <div className="space-y-2 px-4 py-4" data-testid="identity-users-loading" aria-busy="true">
        {[0, 1, 2].map((i) => (
          <div key={i} className="h-9 animate-pulse rounded-md border border-border/60 bg-surface-sunken motion-reduce:animate-none" />
        ))}
      </div>
    );
  } else if (rows.length === 0) {
    body = (
      <div className="flex items-start gap-3 px-4 py-5 text-xs text-ink-muted" data-testid="identity-users-empty">
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
      <div data-testid="identity-users">
        <DataTable<UserRowData>
          flush
          stacked
          layout="fixed"
          ariaLabel={`People who can sign in to ${envName}`}
          columns={columns}
          data={rows}
          getRowKey={(row) => row.id}
          getRowProps={(row) => ({ "data-testid": `identity-user-${row.id}` })}
          getRowAction={(row) => () => open(row.user)}
          getRowLabel={(row) => `Open ${row.user.email}`}
          onSort={(key, dir) => setSort({ key: key as SortKey, dir })}
        />
      </div>
    );
  }

  const shown = loaded.length;
  return (
    <>
      <Panel
        title="Users"
        description="Everyone who can sign in to this environment's applications."
        actions={
          serving &&
          total !== null && (
            <span className="text-xs text-ink-muted" data-testid="identity-users-total">
              {total.toLocaleString()} {total === 1 ? "user" : "users"}
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
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-muted" aria-hidden="true" />
              <input
                type="search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Search by email"
                maxLength={200}
                data-testid="identity-users-search"
                className="w-full rounded-md border border-input bg-surface-sunken py-1.5 pl-8 pr-2 text-sm text-ink placeholder:text-ink-muted focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring"
              />
            </label>
            <div role="group" aria-label="Filter by state" className="inline-flex rounded-lg border border-border bg-surface-sunken p-0.5">
              {STATE_FILTERS.map((option) => (
                <button
                  key={option.label}
                  type="button"
                  aria-pressed={state === option.value}
                  onClick={() => setState(option.value)}
                  className={cn(
                    "rounded-md px-2.5 py-1 text-xs transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    state === option.value ? "bg-surface font-medium text-ink shadow-sm" : "text-ink-muted hover:text-ink"
                  )}
                >
                  {option.label}
                </button>
              ))}
            </div>
          </div>
        )}
        {body}
        {serving && rows.length > 0 && (
          <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border px-4 py-2 text-xs text-ink-muted" data-testid="identity-users-pager">
            <span>
              {total !== null && shown < total ? `Showing ${shown.toLocaleString()} of ${total.toLocaleString()}` : `Showing all ${shown.toLocaleString()}`}
              {sort && users.hasNextPage && <span data-testid="identity-users-sort-note"> · sorted within the loaded {shown.toLocaleString()}</span>}
            </span>
            {users.hasNextPage && (
              <Button variant="outline" size="sm" onClick={() => void users.fetchNextPage()} loading={users.isFetchingNextPage} data-testid="identity-users-more">
                Load more
              </Button>
            )}
          </div>
        )}
      </Panel>

      {canManage && serving && (
        <InvitePanel orgId={orgId} envId={envId} envName={envName} roles={roles} canEmail={canEmail} onIssued={setIssued} lockedReason={lockedReason} />
      )}

      <UserDetail
        open={!!opened.userId}
        user={openUser}
        loading={!openUser && fetched.isLoading}
        failed={!openUser && !!fetched.error}
        editing={opened.editing}
        onEditingChange={(editing) => setOpened({ userId: opened.userId, editing })}
        onClose={close}
        orgId={orgId}
        envId={envId}
        roles={roles}
        reportsSignIns={reportsSignIns}
        verbs={verbs}
      />

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

const shortDate = (ms: number) => new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });

function LastSignIn({ user, reportsSignIns }: { user: IdentityUser; reportsSignIns: boolean }) {
  if (user.lastSignInAtMs) {
    return (
      <time data-testid="identity-user-last-sign-in" dateTime={new Date(user.lastSignInAtMs).toISOString()} title={new Date(user.lastSignInAtMs).toLocaleString()}>
        {formatAgo(user.lastSignInAtMs)}
      </time>
    );
  }
  return reportsSignIns ? <span data-testid="identity-user-last-sign-in">Never</span> : <span aria-label="not reported">—</span>;
}

/** Roles as quiet tags; a declared one carries a lock and says where it is managed. `max` folds the rest into "+n". */
function RoleTags({ roles, declared, max }: { roles: string[]; declared: Set<string>; max?: number }) {
  if (roles.length === 0) return <span className="text-xs text-ink-muted">none</span>;
  const shown = max ? roles.slice(0, max) : roles;
  const rest = roles.length - shown.length;
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-1">
      {shown.map((role) => (
        <span
          key={role}
          title={declared.has(role) ? DECLARED_ROLE_NOTE : undefined}
          className="inline-flex max-w-full items-center gap-1 truncate rounded border border-border/60 px-1.5 py-px font-mono text-2xs text-ink-muted"
        >
          {declared.has(role) && <Lock className="h-2.5 w-2.5 shrink-0" aria-label="declared" />}
          {role}
        </span>
      ))}
      {rest > 0 && (
        <span className="text-2xs text-ink-muted" title={roles.slice(shown.length).join(", ")}>
          +{rest}
        </span>
      )}
    </span>
  );
}

interface UserVerbs {
  canManage: boolean;
  canEmail: boolean;
  /** Why every verb is off (a read-only console), when it is. */
  lockedReason?: string;
  busy: boolean;
  declared: Set<string>;
  open: (user: IdentityUser, editing?: boolean) => void;
  reset: (user: IdentityUser) => void;
  resetLink: (user: IdentityUser) => void;
  unlock: (user: IdentityUser) => void;
  confirm: (kind: Confirm["kind"], user: IdentityUser) => void;
}

/** The admin verbs for one person, as menu items: the same list the row menu and the detail show. */
function adminGroups(user: IdentityUser, verbs: UserVerbs): MenuGroup[] {
  if (!verbs.canManage) return [];
  const off = !!verbs.lockedReason;
  const holdsDeclared = user.roles.some((role) => verbs.declared.has(role));
  return [
    {
      label: verbs.lockedReason,
      items: [
        { label: "Edit roles", onClick: () => verbs.open(user, true), disabled: off, testId: "identity-user-edit-roles" },
        ...(verbs.canEmail ? [{ label: "Email reset", onClick: () => verbs.reset(user), disabled: off || verbs.busy }] : []),
        { label: "Get reset link", icon: <Link2 className="h-3.5 w-3.5" />, onClick: () => verbs.resetLink(user), disabled: off || verbs.busy },
        user.state === "locked"
          ? { label: "Unlock", onClick: () => verbs.unlock(user), disabled: off || verbs.busy }
          : { label: "Lock", onClick: () => verbs.confirm("lock", user), disabled: off },
        { label: "End sessions", onClick: () => verbs.confirm("revoke", user), disabled: off },
      ],
    },
    {
      items: [
        {
          label: "Delete",
          variant: "danger",
          onClick: () => verbs.confirm("delete", user),
          disabled: off || holdsDeclared,
          hint: holdsDeclared ? "declared role" : undefined,
        },
      ],
    },
  ];
}

function UserMenu({ user, envId, verbs }: { user: IdentityUser; envId: string; verbs: UserVerbs }) {
  const appData = useIdentityAppDataHref(envId, user);
  const admin = adminGroups(user, verbs);
  // A menu holding only "Open details" repeats the row's own click: a member with no data link gets none.
  if (admin.length === 0 && !appData) return null;
  const groups: MenuGroup[] = [
    {
      items: [
        { label: "Open details", onClick: () => verbs.open(user) },
        ...(appData ? [{ label: "App data", href: appData, icon: <Database className="h-3.5 w-3.5" /> }] : []),
      ],
    },
    ...admin,
  ];
  return (
    <span data-testid="identity-user-actions" className="inline-flex">
      <DropdownMenu
        trigger={<MoreHorizontal className="h-4 w-4" aria-hidden="true" />}
        triggerLabel={`Actions for ${user.email}`}
        triggerClassName="inline-flex h-7 w-7 items-center justify-center rounded-md text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        groups={groups}
        menuClassName="min-w-[12rem]"
      />
    </span>
  );
}

/**
 * One person, opened from their row: who they are, when they joined and last
 * signed in, where their data is, and the verbs — with the role editor inline.
 */
function UserDetail({
  open,
  user,
  loading,
  failed,
  editing,
  onEditingChange,
  onClose,
  orgId,
  envId,
  roles,
  reportsSignIns,
  verbs,
}: {
  open: boolean;
  user: IdentityUser | null;
  loading: boolean;
  failed: boolean;
  editing: boolean;
  onEditingChange: (editing: boolean) => void;
  onClose: () => void;
  orgId: string;
  envId: string;
  roles: IdentityRole[];
  reportsSignIns: boolean;
  verbs: UserVerbs;
}) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={user?.email ?? "User"}
      description={user && user.displayName && user.displayName !== user.email ? user.displayName : undefined}
      size="md"
      sheetOnMobile
      // A drawer on the right on a wide screen (the list stays in view behind it); a sheet on a phone.
      overlayClassName="sm:justify-end sm:p-0!"
      className="sm:h-full sm:max-h-none! sm:rounded-none! sm:border-l sm:border-border"
    >
      <div data-testid="identity-user-detail">
        {user ? (
          <UserDetailBody user={user} onClose={onClose} editing={editing} onEditingChange={onEditingChange} orgId={orgId} envId={envId} roles={roles} reportsSignIns={reportsSignIns} verbs={verbs} />
        ) : failed ? (
          <p role="alert" className="text-sm text-danger-ink">
            Could not open this person in this environment. The server refused the lookup or they are no longer here.
          </p>
        ) : (
          <p className="text-sm text-ink-muted">{loading ? "Loading…" : "Not found."}</p>
        )}
      </div>
    </Modal>
  );
}

/** The verbs that ask first: their confirmation replaces the detail rather than stacking on it. */
const CONFIRMS = new Set(["Lock", "End sessions", "Delete"]);

function UserDetailBody({
  user,
  onClose,
  editing,
  onEditingChange,
  orgId,
  envId,
  roles,
  reportsSignIns,
  verbs,
}: {
  user: IdentityUser;
  onClose: () => void;
  editing: boolean;
  onEditingChange: (editing: boolean) => void;
  orgId: string;
  envId: string;
  roles: IdentityRole[];
  reportsSignIns: boolean;
  verbs: UserVerbs;
}) {
  const appData = useIdentityAppDataHref(envId, user);
  const groups = adminGroups(user, verbs);
  const at = (ms: number | null) => (ms ? `${new Date(ms).toLocaleString()} (${formatAgo(ms)})` : null);
  return (
    <div className="space-y-5">
      <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2.5 text-sm">
        <dt className="text-xs text-ink-muted">State</dt>
        <dd>
          <Badge label={USER_STATE_LABEL[user.state]} variant={USER_STATE_VARIANT[user.state]} size="sm" />
        </dd>
        <dt className="text-xs text-ink-muted">Roles</dt>
        <dd>
          <RoleTags roles={user.roles} declared={verbs.declared} />
        </dd>
        <dt className="text-xs text-ink-muted">Added</dt>
        <dd className="text-ink">{at(user.createdAtMs) ?? "—"}</dd>
        <dt className="text-xs text-ink-muted">Last sign-in</dt>
        <dd className="text-ink">{at(user.lastSignInAtMs) ?? (reportsSignIns ? "Never" : "Not reported")}</dd>
        <dt className="text-xs text-ink-muted">User id</dt>
        <dd className="break-all font-mono text-xs text-ink">{user.userId}</dd>
      </dl>
      {appData && (
        <Link href={appData} className="inline-flex items-center gap-1.5 text-sm font-medium text-primary-ink hover:underline" data-testid="identity-user-app-data">
          <Database className="h-3.5 w-3.5" aria-hidden="true" />
          Their rows in the app&apos;s data
        </Link>
      )}
      {groups.length > 0 && (
        <section className="space-y-2" aria-label="Manage">
          {verbs.lockedReason && <p className="text-xs text-ink-muted">{verbs.lockedReason}: these are turned off.</p>}
          <div className="flex flex-wrap gap-2" data-testid="identity-user-detail-actions">
            {groups.flatMap((group) => group.items).map((item) =>
              item.label === "Edit roles" ? (
                <Button key={item.label} variant="outline" size="sm" disabled={item.disabled} title={verbs.lockedReason} aria-expanded={editing} onClick={() => onEditingChange(!editing)}>
                  Edit roles
                </Button>
              ) : (
                <Button
                  key={item.label}
                  variant="outline"
                  size="sm"
                  className={cn(item.variant === "danger" && "text-danger-ink!")}
                  disabled={item.disabled}
                  title={verbs.lockedReason ?? (item.hint ? `Holds a declared role (${DECLARED_ROLE_NOTE}); the next reconcile would recreate them` : undefined)}
                  onClick={() => {
                    if (CONFIRMS.has(item.label)) onClose();
                    item.onClick?.();
                  }}
                >
                  {item.label}
                </Button>
              )
            )}
          </div>
          {editing && !verbs.lockedReason && <RoleEditor user={user} roles={roles} orgId={orgId} envId={envId} onDone={() => onEditingChange(false)} />}
        </section>
      )}
    </div>
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
    <div className="space-y-3 rounded-md border border-border/60 bg-surface-sunken p-3" data-testid="identity-role-editor">
      <RoleChecklist roles={roles} selected={selected} onChange={setSelected} heldDeclared={new Set(user.roles)} />
      {save.error && (
        <p role="alert" className="text-xs text-danger-ink">
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
    return <p className="text-xs text-ink-muted">This environment declares no roles.</p>;
  }
  const toggle = (key: string) => {
    const next = new Set(selected);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    onChange(next);
  };
  return (
    <fieldset className="space-y-1.5">
      <legend className="mb-1 text-xs font-medium text-ink">Roles</legend>
      {roles.map((role) => (
        <label
          key={role.key}
          className={cn("flex items-start gap-2 text-xs", role.declared ? "text-ink-muted" : "text-ink")}
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
  lockedReason,
}: {
  orgId: string;
  envId: string;
  envName: string;
  roles: IdentityRole[];
  canEmail: boolean;
  onIssued: (issued: IssuedCode) => void;
  lockedReason?: string;
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
          <span className="text-xs font-medium text-ink">Email</span>
          <input
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            onBlur={() => setTouched(true)}
            maxLength={254}
            placeholder="person@example.com"
            aria-invalid={touched && !valid}
            data-testid="identity-invite-email"
            className="w-full max-w-md rounded-md border border-input bg-surface-sunken px-2.5 py-1.5 text-sm text-ink placeholder:text-ink-muted focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring"
          />
          {touched && !valid && email && <span className="text-xs text-danger-ink">That doesn&apos;t look like an email address.</span>}
        </label>
        <RoleChecklist roles={roles} selected={selected} onChange={setSelected} heldDeclared={new Set()} />
        {canEmail && (
          <label className="flex items-start gap-2 text-xs text-ink">
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
          <p role="alert" className="text-xs text-danger-ink">
            {identityErrorMessage(invite.error) || "Couldn't send the invitation."}
          </p>
        )}
        <div className="flex items-center gap-2">
          <Button
            type="submit"
            size="sm"
            loading={invite.isPending}
            disabled={!valid || !!lockedReason}
            title={lockedReason}
            leftIcon={returnCode ? <Link2 className="h-3.5 w-3.5" /> : <MailPlus className="h-3.5 w-3.5" />}
          >
            {returnCode ? "Invite and get link" : "Send invitation"}
          </Button>
          <span className="inline-flex items-center gap-1 text-xs text-ink-muted">
            <ShieldCheck className="h-3 w-3" aria-hidden="true" />
            Recorded in your organization&apos;s audit log
          </span>
        </div>
      </form>
    </Panel>
  );
}
