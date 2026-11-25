// Copyright (c) 2025 Reliant Labs

/**
 * Settings › API tokens › CLI logins in the forge shell: every `forge cloud login`
 * the person holds, on which device, when it was last used and when it ends,
 * with Revoke to end one at once (services/controlPlane/cliLogins.ts).
 *
 * A CLI login acts as the person on its device for up to 90 days, so one on a
 * machine they no longer use, or do not recognise, is a credential to end
 * here rather than wait out (reliant#1831, option B: visible and revocable).
 * The server answers only a browser session; anything else gets an error,
 * which this section reports instead of an empty list.
 */

import {
  accessTokenServiceKeys,
  useListMyCLILogins,
  useRevokeMyCLILogin,
} from "@/hooks/controlplane/access-token-service-hooks_gen";
import { formatRelativeTime } from "@/lib/relativeTime";
import { cliClientLabel, type CliLogin, liveCliLogins } from "@/services/controlPlane/cliLogins";
import type { ListMyCLILoginsResponse } from "@/gen/controlplane/services/access_token/v1/access_token_pb";

// Hoisted so the mapping reruns only when the answer changes.
const selectLogins = (res: ListMyCLILoginsResponse): CliLogin[] => liveCliLogins(res);

export function CliLoginsSection() {
  const logins = useListMyCLILogins({}, { retry: false, select: selectLogins });
  // Refreshes the CLI logins alone, not the service's every query.
  const revoke = useRevokeMyCLILogin({
    invalidate: (qc) => void qc.invalidateQueries({ queryKey: accessTokenServiceKeys.listMyCLILogins.all }),
  });

  return (
    <section className="rounded-lg border border-border bg-card" data-testid="cli-logins">
      <div className="border-b border-border px-4 py-3">
        <h3 className="text-sm font-medium">CLI logins</h3>
        <p className="text-xs text-muted-foreground">
          Each <code>forge cloud login</code> acts as you on that device until it expires. Revoke one you no longer use or do not
          recognise: its next request is refused.
        </p>
      </div>
      {logins.isPending ? (
        <p className="px-4 py-3 text-sm text-muted-foreground" role="status">
          Loading CLI logins…
        </p>
      ) : logins.isError ? (
        <p className="px-4 py-3 text-sm text-muted-foreground" role="alert">
          Could not load your CLI logins.
        </p>
      ) : logins.data.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted-foreground">No CLI logins.</p>
      ) : (
        <ul className="divide-y divide-border">
          {logins.data.map((l) => (
            <li key={l.id} className="flex items-center gap-3 px-4 py-3 text-sm" data-testid={`cli-login-${l.id}`}>
              <div className="min-w-0 flex-1">
                <div className="font-medium">{l.deviceName || "Unnamed device"}</div>
                <div className="truncate text-xs text-muted-foreground">
                  {cliClientLabel(l.clientId)}
                  {l.createdAt ? ` · signed in ${formatRelativeTime(l.createdAt.toISOString())}` : ""}
                  {l.lastUsedAt ? ` · used ${formatRelativeTime(l.lastUsedAt.toISOString())}` : " · never used"}
                  {l.expiresAt ? ` · expires ${l.expiresAt.toLocaleDateString()}` : ""}
                </div>
              </div>
              <button
                type="button"
                onClick={() => revoke.mutate({ id: l.id })}
                disabled={revoke.isPending}
                aria-label={`Revoke the CLI login on ${l.deviceName || "this device"}`}
                className="rounded-md px-2 py-1 text-sm text-destructive hover:bg-destructive/10"
              >
                Revoke
              </button>
            </li>
          ))}
        </ul>
      )}
      {revoke.isError && (
        <p role="alert" className="border-t border-border px-4 py-3 text-sm text-destructive">
          Could not revoke the CLI login. {String((revoke.error as Error)?.message ?? "")}
        </p>
      )}
    </section>
  );
}
