/**
 * Shared back-button header for every `MobileSettingsScreen` sub-screen —
 * the same "sticky header, single back control" pattern `MobileChatScreen`
 * and `MobileDaemonScreen` use elsewhere on this surface.
 *
 * `onBack` clears `MobileSettingsScreen`'s internal selection rather than
 * navigating: `/m/settings` is a single flat route (owned by another
 * agent — see routes.tsx), so drill-in here is component state, not a URL.
 */

import { MobileBackButton, MobileScreenHeader } from "./MobileChrome";

export function MobileSettingsSectionHeader({
  title,
  onBack,
}: {
  title: string;
  onBack: () => void;
}) {
  return (
    <MobileScreenHeader
      title={title}
      leading={
        <MobileBackButton onClick={onBack} label="Back to settings" />
      }
    />
  );
}
