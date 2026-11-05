// Copyright (c) 2025 Reliant Labs

/**
 * /apps in the forge shell: the org's forge apps from the control plane
 * (useApps; no Reliant projects there), each row opening its console. The
 * same list as Reliant's Apps page, without Reliant's area chrome or its
 * "Build an app" chat.
 */

import { AppsBody } from "../Apps/AppsPage";

export function ForgeAppsPage() {
  return <AppsBody variant="forge" />;
}
