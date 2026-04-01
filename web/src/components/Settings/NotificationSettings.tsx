import { useEffect } from "react";
import { Bell, Volume2, VolumeX, AlertTriangle, CheckCircle, XCircle, AppWindow, MessageSquare, BellRing } from "lucide-react";
import { Toggle } from "../ui/Toggle";
import { useNotificationStore, getNotificationSoundOptions } from "../../store/notificationStore";
import { showTestNotification } from "../../lib/notifications";

export function NotificationSettings() {
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

  // Initialize on mount
  useEffect(() => {
    initialize();
  }, [initialize]);

  // Refresh permission status periodically (in case user changes it in system settings)
  useEffect(() => {
    const interval = setInterval(refreshPermission, 5000);
    return () => clearInterval(interval);
  }, [refreshPermission]);

  // Handle enabling notifications (request permission if needed)
  const handleEnableNotifications = async (enabled: boolean) => {
    if (enabled && permission === "default") {
      // Request permission first
      const newPermission = await requestPermission();
      if (newPermission === "granted") {
        await setNotificationsEnabled(true);
      }
      // If denied, don't enable
    } else if (enabled && permission === "denied") {
      // Can't enable if permission was denied - show instructions
      // Just set the preference, the UI will show the warning
      await setNotificationsEnabled(true);
    } else {
      await setNotificationsEnabled(enabled);
    }
  };

  // Handle test notification
  const handleTestNotification = async () => {
    try {
      const soundOptions = await getNotificationSoundOptions();
      showTestNotification(soundOptions);
    } catch (error) {
      console.error("Failed to get notification sound options:", error);
      // Still try to show notification with default options
      showTestNotification();
    }
  };

  // Permission status indicator
  const renderPermissionStatus = () => {
    if (!isSupported) {
      return (
        <div className="flex items-center gap-2 text-amber-500">
          <AlertTriangle className="w-4 h-4" />
          <span className="text-sm">Notifications not supported in this browser</span>
        </div>
      );
    }

    switch (permission) {
      case "granted":
        return (
          <div className="flex items-center gap-2 text-green-500">
            <CheckCircle className="w-4 h-4" />
            <span className="text-sm">Permission granted</span>
          </div>
        );
      case "denied":
        return (
          <div className="flex items-center gap-2 text-red-500">
            <XCircle className="w-4 h-4" />
            <span className="text-sm">Permission denied - enable in system settings</span>
          </div>
        );
      default:
        return (
          <div className="flex items-center gap-2 text-muted-foreground">
            <AlertTriangle className="w-4 h-4" />
            <span className="text-sm">Permission not requested yet</span>
          </div>
        );
    }
  };

  if (!initialized) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-2 mb-2">
          <Bell className="w-5 h-5" />
          <h2 className="text-lg font-semibold">Notification Settings</h2>
        </div>
        <p className="text-sm text-muted-foreground">Loading...</p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <div className="flex items-center gap-2 mb-2">
          <Bell className="w-5 h-5" />
          <h2 className="text-lg font-semibold">Notification Settings</h2>
        </div>
        <p className="text-sm text-muted-foreground">
          Get notified when workflows complete or need your attention.
        </p>
      </div>

      <div className="space-y-4">
        {/* Permission Status */}
        <div className="p-4 border border-border rounded-lg bg-card">
          <div className="flex items-center justify-between">
            <div>
              <h3 className="text-sm font-medium mb-1">Browser Permission</h3>
              {renderPermissionStatus()}
            </div>
            {permission === "default" && isSupported && (
              <button
                onClick={() => requestPermission()}
                className="px-3 py-1.5 text-sm bg-primary text-primary-foreground rounded-md hover:bg-primary/90 transition-colors"
              >
                Request Permission
              </button>
            )}
          </div>
        </div>

        {/* Notifications Toggle */}
        <div className="flex items-start justify-between p-4 border border-border rounded-lg bg-card">
          <div className="flex-1 pr-4">
            <h3 className="text-sm font-medium mb-1">Desktop Notifications</h3>
            <p className="text-xs text-muted-foreground">
              Show OS notifications when workflows complete or need approval.
            </p>
          </div>
          <Toggle
            checked={notificationsEnabled}
            onChange={handleEnableNotifications}
            label={`${notificationsEnabled ? "Disable" : "Enable"} notifications`}
            disabled={!isSupported}
          />
        </div>

        {/* Warning if enabled but permission denied */}
        {notificationsEnabled && permission === "denied" && (
          <div className="flex items-start gap-3 p-4 border border-red-500/30 bg-red-500/5 rounded-lg">
            <XCircle className="w-5 h-5 text-red-500 flex-shrink-0 mt-0.5" />
            <div className="flex-1">
              <p className="text-sm text-foreground font-medium mb-1">
                Permission Required
              </p>
              <p className="text-xs text-muted-foreground">
                Notification permission was denied. To enable notifications, you need to allow them in your browser or system settings, then refresh this page.
              </p>
            </div>
          </div>
        )}

        {/* When to Notify Section */}
        {notificationsEnabled && permission === "granted" && (
          <div className="p-4 border border-border rounded-lg bg-card space-y-4">
            <h3 className="text-sm font-medium">When to Notify</h3>
            
            {/* Notify when app is unfocused */}
            <div className="flex items-start justify-between">
              <div className="flex-1 pr-4">
                <div className="flex items-center gap-2 mb-1">
                  <AppWindow className="w-4 h-4 text-muted-foreground" />
                  <span className="text-sm">When app is in background</span>
                </div>
                <p className="text-xs text-muted-foreground ml-6">
                  Notify when Reliant window is not focused (you're in another app)
                </p>
              </div>
              <Toggle
                checked={notifyWhenUnfocused}
                onChange={setNotifyWhenUnfocused}
                label="Notify when unfocused"
                disabled={notifyAlways}
              />
            </div>

            {/* Notify when in different chat */}
            <div className="flex items-start justify-between">
              <div className="flex-1 pr-4">
                <div className="flex items-center gap-2 mb-1">
                  <MessageSquare className="w-4 h-4 text-muted-foreground" />
                  <span className="text-sm">When viewing a different chat</span>
                </div>
                <p className="text-xs text-muted-foreground ml-6">
                  Notify when a background chat completes while you're in another chat
                </p>
              </div>
              <Toggle
                checked={notifyWhenDifferentChat}
                onChange={setNotifyWhenDifferentChat}
                label="Notify when in different chat"
                disabled={notifyAlways}
              />
            </div>

            {/* Always notify */}
            <div className="flex items-start justify-between">
              <div className="flex-1 pr-4">
                <div className="flex items-center gap-2 mb-1">
                  <BellRing className="w-4 h-4 text-muted-foreground" />
                  <span className="text-sm">Always notify</span>
                </div>
                <p className="text-xs text-muted-foreground ml-6">
                  Always show notifications, even when viewing the active chat
                </p>
              </div>
              <Toggle
                checked={notifyAlways}
                onChange={setNotifyAlways}
                label="Always notify"
              />
            </div>
          </div>
        )}

        {/* Sound Settings */}
        <div className="flex items-start justify-between p-4 border border-border rounded-lg bg-card">
          <div className="flex-1 pr-4">
            <div className="flex items-center gap-2 mb-1">
              {soundEnabled ? (
                <Volume2 className="w-4 h-4 text-muted-foreground" />
              ) : (
                <VolumeX className="w-4 h-4 text-muted-foreground" />
              )}
              <h3 className="text-sm font-medium">Notification Sound</h3>
            </div>
            <p className="text-xs text-muted-foreground">
              Play the system notification sound when notifications appear.
            </p>
          </div>
          <Toggle
            checked={soundEnabled}
            onChange={setSoundEnabled}
            label={`${soundEnabled ? "Disable" : "Enable"} sound`}
          />
        </div>

        {/* Test Notification Button */}
        <div className="p-4 border border-border rounded-lg bg-card">
          <div className="flex items-center justify-between">
            <div>
              <h3 className="text-sm font-medium mb-1">Test Notification</h3>
              <p className="text-xs text-muted-foreground">
                Send a test notification to verify your settings.
              </p>
            </div>
            <button
              onClick={handleTestNotification}
              disabled={!isSupported || permission !== "granted"}
              className="px-3 py-1.5 text-sm bg-secondary text-secondary-foreground rounded-md hover:bg-secondary/80 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            >
              Send Test
            </button>
          </div>
        </div>

        {/* Information notice */}
        <div className="p-4 border border-border rounded-lg elevation-1">
          <p className="text-xs text-muted-foreground mb-2">
            <strong>What triggers notifications?</strong>
          </p>
          <ul className="text-xs text-muted-foreground list-disc list-inside space-y-1">
            <li>Workflow completes (LLM finishes responding)</li>
            <li>Approval required (tool needs your permission)</li>
          </ul>
          <p className="text-xs text-muted-foreground mt-3">
            Notifications use your system's default sound. You can change the sound in your OS notification settings.
          </p>
        </div>
      </div>
    </div>
  );
}
