// Copyright (c) 2026 Reliant Labs

/**
 * hooks/useCancelOnUnload.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the real hook sends a CancelChat beacon when
 * the page closes on a running chat, straight to the API's URL rather than
 * through the transport. The demo's chats run nowhere and its transport
 * refuses every write, so there is nothing to cancel and no beacon to send.
 */

import type * as Real from "./useCancelOnUnload";

export const useCancelOnUnload: typeof Real.useCancelOnUnload = () => {};
