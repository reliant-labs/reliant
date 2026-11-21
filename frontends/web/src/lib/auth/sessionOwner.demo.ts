/** The Forge demo has no signed-in account and cannot change its owner. */
import type { currentSessionOwner as readSessionOwner } from "./sessionOwner";

const anonymousOwner = Object.freeze({ userId: null, epoch: 0 });

// Shared route code can read the epoch. Missing auth and credential exports
// are intentional: the demo must not gain a production auth dependency.
export const currentSessionOwner: typeof readSessionOwner = () => anonymousOwner;
