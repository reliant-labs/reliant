/**
 * product.app_first_launch: once per desktop install, with the install's id,
 * platform, arch and version, so the desktop funnel has a top that exists
 * before sign-in stitches the install to a user.
 *
 * Electron main owns the install record (electron/src/install-info.js) and
 * keeps the launch pending until told it went out. It is only told when the
 * event could actually leave: telemetry running, analytics consented, and the
 * network up. Anything else leaves it pending for the next launch, so a first
 * launch made offline, or before consent, is reported later rather than lost.
 * `installed_at` carries the install date so a late report still lands on it.
 */

import { clickstackDeliveryActive, trackEvent } from "./analytics";

export async function reportFirstLaunch(): Promise<void> {
  const api = typeof window !== "undefined" ? window.electronAPI : undefined;
  if (!api?.getInstallInfo || !api.markFirstLaunchReported) return;
  if (!clickstackDeliveryActive()) return;
  if (typeof navigator !== "undefined" && navigator.onLine === false) return;

  const info = await api.getInstallInfo();
  if (!info.firstLaunchPending) return;

  trackEvent("app_first_launch", {
    installId: info.installId,
    installedAt: info.installedAt,
    platform: info.platform,
    arch: info.arch,
    version: info.version,
  });
  await api.markFirstLaunchReported();
}
