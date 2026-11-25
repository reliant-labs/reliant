// Connected social accounts (GitHub, Google): the providers the login policy
// declares, each connected or not, plus any link to a provider the policy no
// longer declares (which can still be disconnected).
//
// Connecting leaves for the provider (POST /auth/account/links/start answers
// the URL); the provider returns to the SPA's social-callback page, which
// completes it as a LINK to this session and comes back here. A provider
// account already connected to someone else is refused there (`social_taken`).
// Disconnecting the account's last way in is refused (`last_credential`).

import { useState } from "react";
import { Link2 } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { startLink, unlinkSocial, type Account } from "@/lib/auth/account";
import { settingsPath } from "@/lib/paths";
import { SocialProviderIcon, type SocialProvider } from "../../icons/SocialProviderIcon";
import { useRecentProof } from "./RecentProof";
import { AccountSection, InlineError, InlineSuccess, SectionHeading, accountErrorMessage } from "./parts";

const ICONS: ReadonlySet<string> = new Set<SocialProvider>(["github", "google", "apple"]);

interface Row {
  provider: string;
  label: string;
  connected: boolean;
  userName?: string;
  /** Declared by the policy, so it can be connected again. */
  declared: boolean;
}

function rowsOf(account: Pick<Account, "social" | "links">): Row[] {
  const rows: Row[] = account.social.map((s) => {
    const link = account.links.find((l) => l.provider === s.type);
    return { provider: s.type, label: s.label || s.type, connected: s.connected, userName: link?.userName, declared: true };
  });
  for (const l of account.links) {
    if (rows.some((r) => r.provider === l.provider)) continue;
    rows.push({ provider: l.provider, label: l.label || l.provider, connected: true, userName: l.userName, declared: false });
  }
  return rows;
}

function ProviderIcon({ provider }: { provider: string }) {
  return ICONS.has(provider) ? (
    <SocialProviderIcon provider={provider as SocialProvider} className="h-4 w-4" />
  ) : (
    <Link2 className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
  );
}

export function ConnectedAccountsSection({
  account,
  onChanged,
  returnTo = settingsPath("account"),
}: {
  account: Pick<Account, "social" | "links">;
  onChanged: () => void;
  /** Where the provider round trip comes back to. */
  returnTo?: string;
}) {
  const withRecentProof = useRecentProof();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const rows = rowsOf(account);

  if (rows.length === 0) return null;

  const connect = async (row: Row) => {
    setBusy(row.provider);
    setError(null);
    setNotice(null);
    try {
      const started = await withRecentProof(() => startLink(row.provider, returnTo));
      if (started) {
        // Leaving for the provider: the button stays busy until the page goes.
        window.location.assign(started.redirect);
        return;
      }
    } catch (err) {
      setError(accountErrorMessage(err));
    }
    setBusy(null);
  };

  const disconnect = async (row: Row) => {
    setBusy(row.provider);
    setError(null);
    setNotice(null);
    try {
      const done = await withRecentProof(async () => {
        await unlinkSocial(row.provider);
        return true;
      });
      if (done) {
        setNotice(`${row.label} disconnected.`);
        onChanged();
      }
    } catch (err) {
      setError(accountErrorMessage(err));
    } finally {
      setBusy(null);
    }
  };

  return (
    <AccountSection testId="forge-account-connected">
      <SectionHeading title="Connected accounts" description="Sign in with any account connected here." />
      <ul className="divide-y divide-border-subtle">
        {rows.map((row) => (
          <li key={row.provider} className="flex items-center justify-between gap-3 py-2.5">
            <div className="flex min-w-0 items-center gap-3">
              <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full border border-border/60 bg-background">
                <ProviderIcon provider={row.provider} />
              </span>
              <div className="min-w-0">
                <p className="truncate text-ui text-foreground">{row.label}</p>
                <p className="truncate text-meta text-muted-foreground">
                  {row.connected ? row.userName || "Connected" : "Not connected"}
                </p>
              </div>
            </div>
            {row.connected ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void disconnect(row)}
                loading={busy === row.provider}
                disabled={busy !== null && busy !== row.provider}
                aria-label={`Disconnect ${row.label}`}
              >
                Disconnect
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                onClick={() => void connect(row)}
                loading={busy === row.provider}
                disabled={!row.declared || (busy !== null && busy !== row.provider)}
                aria-label={`Connect ${row.label}`}
              >
                Connect
              </Button>
            )}
          </li>
        ))}
      </ul>
      <InlineError message={error} />
      <InlineSuccess message={notice} />
    </AccountSection>
  );
}
