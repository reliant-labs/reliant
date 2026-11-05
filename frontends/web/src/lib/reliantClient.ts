/**
 * The app's @reliantlabs/sdk client, on the app's own routed transport.
 *
 * The transport already carries everything Reliant-specific (auth, org,
 * deadline, upgrade modal, machine wake, 401 recovery), picks the backend per
 * procedure, and honours the test seam (`setTransport`) and mock mode, so the
 * SDK adds no second chain on top of it. It is registered as the SDK's default
 * client: the app mounts no <ReliantProvider>, there is one client per page.
 */

import { createReliantClient } from "@reliantlabs/sdk/client";
import { setDefaultReliantClient } from "@reliantlabs/sdk/react";
import { transport } from "./connect";

export const reliantClient = createReliantClient({ transport });

setDefaultReliantClient(reliantClient);
