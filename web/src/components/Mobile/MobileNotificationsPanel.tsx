/**
 * Mobile-native "Notifications" panel — the same `useNotificationStore`
 * this reuses is what desktop `NotificationSettings` reads and writes, so a
 * toggle flipped here is the same persisted preference desktop shows. Only
 * the presentation is mobile: single-column rows with a ≥44px tap area
 * instead of desktop's card grid with a 38.5px switch.
 */

import { useEffect } from "react";
import {
  AlertTriangle,
  AppWindow,
  Bell,
  BellRing,
  CheckCircle,
  MessageSquare,
  XCircle,
} from "lucide-react";
import { useNotificationStore, getNotificationSoundOptions } from "../../store/notificationStore";
import { showTestNotification } from "../../lib/notifications";
import { MobileToggleRow } from "./MobileSettingsRow";

function PermissionStatus({
  isSupported,
  permission,
}: {
  isSupported: boolean;
  permission: "granted" | "denied" | "default";
}) {
  if (!isSupported) {
    return (
      <div className="flex items-center gap-2 text-amber-500">
        <AlertTriangle className="h-4 w-4 shrink-0" />
        <span className="text-sm">Notifications not supported in this browser</span>
      </div>
    );
  }
  switch (permission) {
    case "granted":
      return (
        <div className="flex items-center gap-2 text-green-500">
          <CheckCircle className="h-4 w-4 shrink-0" />
          <span className="text-sm font-medium">Permission granted</span>
        </div>
      );
    case "denied":
      return (
        <div className="flex items-center gap-2 text-destructive">
          <XCircle className="h-4 w-4 shrink-0" />
          <span className="text-sm">Permission denied — enable in system settings</span>
        </div>
      );
    default:
      return (
        <div className="flex items-center gap-2 text-muted-foreground">
          <AlertTriangle className="h-4 w-4 shrink-0" />
          <span className="text-sm">Permission not requested yet</span>
        </div>
      );
  }
}

export function MobileNotificationsPanel() {
  const {
    notificationsEnabled,
    soundEnabled,
    notifyWhenUnfocused,
    notifyWhenDifferentChat,
    notifyAlways,
    permission,
    isSupported,
    initialized,
    initialize,
    setNotificationsEnabled,
    setSoundEnabled,
    setNotifyWhenUnfocused,
    setNotifyWhenDifferentChat,
    setNotifyAlways,
    requestPermission,
    refreshPermission,
  } = useNotificationStore();

  useEffect(() => {
    initialize();
  }, [initialize]);

  useEffect(() => {
    const interval = setInterval(refreshPermission, 5000);
    return () => clearInterval(interval);
  }, [refreshPermission]);

  const handleEnableNotifications = async (enabled: boolean) => {
    if (enabled && permission === "default") {
      const newPermission = await requestPermission();
      if (newPermission === "granted") {
        await setNotificationsEnabled(true);
      }
    } else {
      await setNotificationsEnabled(enabled);
    }
  };

  const handleTestNotification = async () => {
    try {
      const soundOptions = await getNotificationSoundOptions();
      showTestNotification(soundOptions);
    } catch (error) {
      console.error("Failed to get notification sound options:", error);
      showTestNotification();
    }
  };

  if (!initialized) {
    return (
      <div className="p-4">
        <p className="text-sm text-muted-foreground">Loading…</p>
      </div>
    );
  }

  return (
    <div className="divide-y divide-border">
      <div className="p-4">
        <div className="mb-1 flex items-center gap-2">
          <Bell className="h-4 w-4 text-muted-foreground" />
          <h3 className="text-sm font-semibold text-foreground">Browser permission</h3>
        </div>
        <PermissionStatus isSupported={isSupported} permission={permission} />
        {permission === "default" && isSupported && (
          <button
            type="button"
            onClick={() => requestPermission()}
            className="mt-3 flex min-h-[44px] w-full items-center justify-center rounded-md bg-primary text-sm font-medium text-primary-foreground active:opacity-80"
          >
            Request permission
          </button>
        )}
      </div>

      <MobileToggleRow
        label="Desktop notifications"
        description="Show OS notifications when workflows complete or need approval."
        checked={notificationsEnabled}
        onChange={handleEnableNotifications}
        disabled={!isSupported}
      />

      {notificationsEnabled && permission === "denied" && (
        <div className="flex items-start gap-3 border-y border-destructive/40 bg-destructive/10 px-4 py-3">
          <XCircle className="mt-0.5 h-5 w-5 shrink-0 text-destructive" />
          <div>
            <p className="text-sm font-medium text-foreground">Permission required</p>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Notification permission was denied. Allow notifications in your browser
              or system settings, then refresh.
            </p>
          </div>
        </div>
      )}

      {notificationsEnabled && permission === "granted" && (
        <>
          <div className="bg-muted/30 px-4 py-2">
            <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              When to notify
            </p>
          </div>
          <MobileToggleRow
            icon={<AppWindow className="h-4 w-4" />}
            label="When app is in background"
            description="Notify when Reliant isn't focused."
            checked={notifyWhenUnfocused}
            onChange={setNotifyWhenUnfocused}
            disabled={notifyAlways}
          />
          <MobileToggleRow
            icon={<MessageSquare className="h-4 w-4" />}
            label="When viewing a different chat"
            description="Notify when a background chat completes."
            checked={notifyWhenDifferentChat}
            onChange={setNotifyWhenDifferentChat}
            disabled={notifyAlways}
          />
          <MobileToggleRow
            icon={<BellRing className="h-4 w-4" />}
            label="Always notify"
            description="Show notifications even in the active chat."
            checked={notifyAlways}
            onChange={setNotifyAlways}
          />
        </>
      )}

      <MobileToggleRow
        label="Notification sound"
        description="Play the system sound when notifications appear."
        checked={soundEnabled}
        onChange={setSoundEnabled}
      />

      <div className="p-4">
        <p className="mb-1 text-sm font-medium text-foreground">Test notification</p>
        <p className="mb-3 text-xs text-muted-foreground">
          Send a test notification to verify your settings.
        </p>
        <button
          type="button"
          onClick={handleTestNotification}
          disabled={!isSupported || permission !== "granted"}
          className="flex min-h-[44px] w-full items-center justify-center rounded-md border border-border text-sm font-medium text-foreground active:bg-muted disabled:opacity-50"
        >
          Send test
        </button>
      </div>

      <div className="bg-muted/30 p-4">
        <p className="mb-2 text-xs text-muted-foreground">
          <strong className="text-foreground">What triggers notifications?</strong>
        </p>
        <ul className="list-inside list-disc space-y-1 text-xs text-muted-foreground">
          <li>Workflow completes (LLM finishes responding)</li>
          <li>Approval required (tool needs your permission)</li>
        </ul>
      </div>
    </div>
  );
}
