// Copyright (c) 2025 Reliant Labs

/**
 * A full page load to a same-origin path. For the one hop where a client-side
 * navigate loses a race: signing out unmounts an AuthGuard, whose effect sends
 * the router to a bare /auth in the same tick. A page load cannot be
 * overtaken by it. A seam so tests do not have to stub `window.location`.
 */
export function hardNavigate(path: string): void {
  window.location.assign(path);
}
