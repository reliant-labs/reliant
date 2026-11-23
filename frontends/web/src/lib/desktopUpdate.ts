/**
 * A desktop-app capability this web bundle needs, missing from the running
 * desktop build.
 *
 * The desktop app loads this bundle from the server, so the bundle can be
 * newer than the Electron preload that exposes `window.electronAPI`. When a
 * bridge the bundle relies on is absent, the only fix is a newer desktop app:
 * callers feature-detect the bridge and throw this, and the UI shows its
 * message, rather than crashing on `undefined is not a function`.
 */
export class DesktopUpdateRequiredError extends Error {
  constructor(feature: string) {
    super(
      `${feature} needs a newer version of the Reliant desktop app. ` +
        'Update the Reliant desktop app, then try again.',
    )
    this.name = 'DesktopUpdateRequiredError'
  }
}

export const isDesktopUpdateRequired = (error: unknown): error is DesktopUpdateRequiredError =>
  error instanceof DesktopUpdateRequiredError
