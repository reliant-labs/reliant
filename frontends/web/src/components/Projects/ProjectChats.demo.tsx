// Copyright (c) 2026 Reliant Labs

/**
 * components/Projects/ProjectChats.tsx in the forge demo's bundle (a demo
 * twin, vite-plugin-demo-bundle.ts): chats are Reliant's, and the demo has no
 * Reliant project to list them for. The real list reads chat-queries and the
 * activity store, which the demo bundle must not contain.
 */

import type * as Real from "./ProjectChats";

export const ProjectChats: typeof Real.ProjectChats = () => <></>;
