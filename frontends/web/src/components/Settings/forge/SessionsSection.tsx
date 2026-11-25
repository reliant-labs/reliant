// Where the account is signed in. Signing one other device out needs no proof;
// signing out every other device does (recent proof). This device is flagged
// and cannot be revoked from here (`current_session`): Sign out does that.

import { useState } from "react";
import { Monitor } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { revokeOtherSessions, revokeSession, type AccountSession } from "@/lib/auth/account";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { useRecentProof } from "./RecentProof";
import { AccountSection, InlineError, InlineSuccess, SectionHeading, accountErrorMessage } from "./parts";

/** "Chrome on macOS" from a user agent; "Unknown device" when there is none. */
export function describeUserAgent(ua: string | undefined): string {
  if (!ua) return "Unknown device";
  if (/Reliant|Electron/i.test(ua)) return `Reliant desktop${osOf(ua) ? ` on ${osOf(ua)}` : ""}`;
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /Firefox\//.test(ua)
      ? "Firefox"
      : /Chrome\//.test(ua)
        ? "Chrome"
        : /Safari\//.test(ua)
          ? "Safari"
          : "";
  const os = osOf(ua);
  if (browser && os) return `${browser} on ${os}`;
  return browser || os || "Unknown device";
}

function osOf(ua: string): string {
  if (/iPhone|iPad|iPod/.test(ua)) return "iOS";
  if (/Android/.test(ua)) return "Android";
  if (/Mac OS X|Macintosh/.test(ua)) return "macOS";
  if (/Windows/.test(ua)) return "Windows";
  if (/Linux/.test(ua)) return "Linux";
  return "";
}

export function SessionsSection({ sessions, onChanged }: { sessions: AccountSession[]; onChanged: () => void }) {
  const withRecentProof = useRecentProof();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const others = sessions.filter((s) => !s.current);
  // This device first, then the most recently seen.
  const ordered = [...sessions].sort(
    (a, b) => Number(b.current) - Number(a.current) || Date.parse(b.lastSeenAt) - Date.parse(a.lastSeenAt),
  );

  const revoke = async (session: AccountSession) => {
    setBusy(session.handle);
    setError(null);
    setNotice(null);
    try {
      await revokeSession(session.handle);
      setNotice(`${describeUserAgent(session.userAgent)} signed out.`);
      onChanged();
    } catch (err) {
      setError(accountErrorMessage(err));
    } finally {
      setBusy(null);
    }
  };

  const revokeOthers = async () => {
    setBusy("others");
    setError(null);
    setNotice(null);
    try {
      const ended = await withRecentProof(() => revokeOtherSessions());
      if (ended !== undefined) {
        setNotice(ended === 1 ? "One other device signed out." : `${ended} other devices signed out.`);
        onChanged();
      }
    } catch (err) {
      setError(accountErrorMessage(err));
    } finally {
      setBusy(null);
    }
  };

  return (
    <AccountSection testId="forge-account-sessions">
      <SectionHeading
        title="Where you're signed in"
        description="Sign out a device you don't recognize."
        action={
          others.length > 0 && (
            <Button variant="outline" size="sm" onClick={() => void revokeOthers()} loading={busy === "others"} disabled={busy !== null && busy !== "others"}>
              Sign out other devices
            </Button>
          )
        }
      />
      <ul className="divide-y divide-border-subtle">
        {ordered.map((session) => (
          <li key={session.handle} className="flex items-center justify-between gap-3 py-2.5" data-current={session.current || undefined}>
            <div className="flex min-w-0 items-center gap-3">
              <Monitor className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <div className="min-w-0">
                <p className="truncate text-ui text-foreground">
                  {describeUserAgent(session.userAgent)}
                  {session.current && (
                    <span className="ml-2 inline-flex items-center gap-1.5 text-meta text-muted-foreground">
                      <span aria-hidden="true" className="h-2 w-2 rounded-full bg-primary" />
                      This device
                    </span>
                  )}
                </p>
                <p className="truncate text-meta text-muted-foreground" title={formatAbsoluteTime(session.lastSeenAt)}>
                  {session.current ? "Active now" : `Last active ${formatRelativeTime(session.lastSeenAt) || "unknown"}`}
                </p>
              </div>
            </div>
            {!session.current && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void revoke(session)}
                loading={busy === session.handle}
                disabled={busy !== null && busy !== session.handle}
                aria-label={`Sign out ${describeUserAgent(session.userAgent)}`}
              >
                Sign out
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
