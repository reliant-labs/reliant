// Copyright (c) 2026 Reliant Labs

/**
 * The shop story's signed-in person, Alex Demo (workspace.ts re-exports it):
 * the pack's GetCurrentUser, the screenshot harness's user, and the public
 * demo's visitor (demoShellRoutes.tsx mounts the shell's session with it), so
 * the nav's account row and the pack agree.
 *
 * A module of its own, with no imports: the demo shell reads it in its entry
 * chunk, which must not carry the pack's reliant protos with it.
 */
export const APP_USER = { id: "00000000-0000-4000-8000-00000000a1ec", email: "alex@example.com", name: "Alex Demo" } as const;
