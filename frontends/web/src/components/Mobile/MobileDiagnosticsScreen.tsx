/**
 * Settings → Diagnostics: what went wrong, on a device with no devtools.
 *
 * Shows this build and the server's, the live update stream's state (the
 * thing behind "my chat stopped updating"), the network, and the recent
 * client errors `lib/clientDiagnostics.ts` keeps on the device. "Copy
 * diagnostics" puts all of it on the clipboard as plain text for a bug
 * report (`lib/diagnosticsReport.ts` has the format).
 *
 * Reachable from `/m/settings` and directly at `/m/diagnostics`, so support
 * can send someone a link even when the screen they are stuck on is broken.
 */

import { useEffect, useState } from "react";
import { useShallow } from "zustand/react/shallow";
import { Check, Copy, Trash2 } from "lucide-react";
import { serverVersionLabel, useVersionInfo } from "@/hooks/useVersionInfo";
import { clearClientErrors, useClientErrors, type ClientErrorEntry } from "@/lib/clientDiagnostics";
import { formatDiagnosticsReport, type DiagnosticsFacts } from "@/lib/diagnosticsReport";
import { useSurface } from "@/lib/surfaceContext";
import { useAuthStore } from "@/store/authStore";
import { useGlobalUpdatesStore } from "@/store/globalUpdatesStore";
import { useProjectStore } from "@/store/projectStore";
import { cn } from "@/lib/utils";
import { MOBILE_PRIMARY_ACTION, MobileCardGroup } from "./MobileChrome";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";
import { relativeTime, relativeTimeFromMs } from "./relativeTime";

const STREAM_LABEL: Record<string, string> = {
  connected: "Connected",
  connecting: "Connecting…",
  disconnected: "Disconnected",
  error: "Error",
};

function useOnline(): boolean {
  const [online, setOnline] = useState(() => (typeof navigator === "undefined" ? true : navigator.onLine));
  useEffect(() => {
    const update = () => setOnline(navigator.onLine);
    window.addEventListener("online", update);
    window.addEventListener("offline", update);
    return () => {
      window.removeEventListener("online", update);
      window.removeEventListener("offline", update);
    };
  }, []);
  return online;
}

function Row({ label, value, tone }: { label: string; value: string; tone?: "ok" | "bad" }) {
  return (
    <div className="flex min-h-[44px] items-center justify-between gap-4 border-b border-border px-4 py-2.5 last:border-b-0">
      <span className="shrink-0 text-sm text-muted-foreground">{label}</span>
      <span
        className={cn(
          "min-w-0 truncate text-right font-mono text-xs",
          tone === "ok" ? "text-success-ink" : tone === "bad" ? "text-destructive-ink" : "text-foreground",
        )}
        title={value}
      >
        {value}
      </span>
    </div>
  );
}

function ErrorRow({ entry }: { entry: ClientErrorEntry }) {
  return (
    <li className="border-b border-border px-4 py-3 last:border-b-0" data-testid="diagnostics-error">
      <div className="flex items-center justify-between gap-3 text-xs text-muted-foreground">
        <span>
          {entry.source}
          {entry.count > 1 ? ` ×${entry.count}` : ""} · <span className="font-mono">{entry.path}</span>
        </span>
        <time dateTime={entry.at}>{relativeTime(entry.at)}</time>
      </div>
      <p className="mt-1 select-text break-words font-mono text-xs text-foreground">{entry.message}</p>
    </li>
  );
}

export function MobileDiagnosticsScreen({ onBack }: { onBack: () => void }) {
  const { app, server, release } = useVersionInfo();
  const surface = useSurface();
  const online = useOnline();
  const errors = useClientErrors();
  const stream = useGlobalUpdatesStore(
    useShallow((s) => ({
      status: s.connectionStatus,
      lastSequence: s.lastSequence,
      subscribedChatId: s.subscribedChatId,
      daemonLastSeen: s.daemonLastSeen,
    })),
  );
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const project = useProjectStore((s) => s.currentProject);
  const [copied, setCopied] = useState<"idle" | "copied" | "failed">("idle");

  useEffect(() => {
    if (copied === "idle") return;
    const timer = window.setTimeout(() => setCopied("idle"), 2000);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const facts = (): DiagnosticsFacts => ({
    app,
    server: serverVersionLabel(server),
    release,
    url: window.location.href,
    userAgent: navigator.userAgent,
    viewport: `${window.innerWidth}×${window.innerHeight} @${window.devicePixelRatio}x`,
    online,
    surface,
    stream,
    userId,
    projectId: project?.id ?? null,
    generatedAt: new Date().toISOString(),
  });

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(formatDiagnosticsReport(facts(), errors));
      setCopied("copied");
    } catch {
      setCopied("failed");
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="mobile-diagnostics">
      <MobileSettingsSectionHeader title="Diagnostics" onBack={onBack} />
      <div className="min-h-0 flex-1 space-y-6 overflow-y-auto px-4 py-4 pb-[calc(2.5rem+env(safe-area-inset-bottom))]">
        <button type="button" onClick={() => void copy()} className={cn(MOBILE_PRIMARY_ACTION, "w-full")}>
          {copied === "copied" ? <Check className="h-4 w-4" aria-hidden /> : <Copy className="h-4 w-4" aria-hidden />}
          {copied === "copied" ? "Copied" : copied === "failed" ? "Could not copy — select the text below" : "Copy diagnostics"}
        </button>
        <span className="sr-only" role="status">
          {copied === "copied" ? "Diagnostics copied" : ""}
        </span>

        <MobileCardGroup label="App">
          <Row label="This app" value={app ?? "unknown"} />
          <Row label="Server" value={serverVersionLabel(server)} tone={server.state === "unreachable" ? "bad" : undefined} />
          {release && <Row label="Release" value={release} />}
        </MobileCardGroup>

        <MobileCardGroup label="Connection">
          <Row label="Network" value={online ? "Online" : "Offline"} tone={online ? "ok" : "bad"} />
          <Row
            label="Update stream"
            value={STREAM_LABEL[stream.status] ?? stream.status}
            tone={stream.status === "connected" ? "ok" : stream.status === "error" ? "bad" : undefined}
          />
          <Row label="Last sequence" value={String(stream.lastSequence)} />
          <Row label="Subscribed chat" value={stream.subscribedChatId ?? "none"} />
          <Row
            label="Machine heartbeat"
            value={stream.daemonLastSeen ? relativeTimeFromMs(stream.daemonLastSeen * 1000) : "none yet"}
          />
        </MobileCardGroup>

        <section>
          <div className="mb-2 flex items-center justify-between px-1">
            <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Recent errors <span className="tabular-nums">{errors.length}</span>
            </h2>
            {errors.length > 0 && (
              <button
                type="button"
                onClick={clearClientErrors}
                className="flex min-h-[44px] items-center gap-1.5 rounded-md px-2 text-xs text-muted-foreground active:bg-foreground/5"
              >
                <Trash2 className="h-3.5 w-3.5" aria-hidden />
                Clear
              </button>
            )}
          </div>
          <div className="overflow-hidden rounded-lg elevation-1">
            {errors.length === 0 ? (
              <p className="px-4 py-4 text-sm text-muted-foreground">No errors recorded in this tab.</p>
            ) : (
              <ul>
                {[...errors].reverse().map((entry, index) => (
                  <ErrorRow key={`${entry.at}:${index}`} entry={entry} />
                ))}
              </ul>
            )}
          </div>
          <p className="mt-2 px-1 text-xs leading-relaxed text-muted-foreground">
            Kept on this device for this tab only, and never sent anywhere. Copy them into a bug report.
          </p>
        </section>
      </div>
    </div>
  );
}
