/**
 * Thread visualization components
 */

// Primary thread view
export { InterleavedTimeline } from "./InterleavedTimeline";

// Thread tabs component (for ChatHeader integration)
export { ThreadTabs } from "./ThreadTabs";

// Hooks and utilities
export { useThreads, useMessagesByThread } from "./useThreads";
export type { ThreadInfo } from "./useThreads";
export { getThreadColor, formatNodeId } from "./threadUtils";

// Activity indicators
export { ActivityIndicator, CompactActivityIndicator } from "./ActivityIndicator";
export { getActivitySteps, getActivityStepsForThread, getStepDisplayName } from "./activityIndicators";
export type { ActivityStep } from "./activityIndicators";
