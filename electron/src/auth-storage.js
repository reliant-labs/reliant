const path = require('path');
const fs = require('fs');
const { app, safeStorage } = require('electron');
const log = require('./logger');

/**
 * Outcome of a stored-auth read.
 *
 * `loadStoredAuth()` used to answer this question with a bare `null`, which
 * conflated three very different states: "this user is not signed in", "the
 * blob exists but its key is gone", and "the OS keystore is not answering
 * right now". The renderer's Supabase storage adapter turns any `null` into
 * "no session", so an unreadable blob presented as a signed-out user — except
 * the in-memory Zustand store still held a session from `onAuthStateChange`,
 * so the UI stayed signed IN while every outbound RPC went out with no
 * Authorization header and came back "missing authorization token".
 *
 * Naming the three states is what lets each one be handled correctly:
 * `empty` is normal, `unreadable` is an incident.
 */
const AUTH_LOAD_OK = 'ok';
const AUTH_LOAD_EMPTY = 'empty';
const AUTH_LOAD_UNREADABLE = 'unreadable';

/**
 * Why a blob could not be turned into a session.
 *
 * The split that matters is whether a FUTURE run could succeed:
 *
 *   - `decrypt_failed` / `corrupt` / `invalid` are unrecoverable by
 *     construction. The OS key that produced the ciphertext is gone (keychain
 *     item recreated, OS reinstall, profile migration), or the plaintext is
 *     not a session. No later run decrypts it either, so the blob is cleared
 *     once and the user must genuinely re-authenticate.
 *   - `encryption_unavailable` and `read_failed` are TRANSIENT — a locked
 *     Linux keyring or a transient EACCES. Clearing on those would destroy a
 *     session that is still perfectly good, so they never delete anything.
 */
const AUTH_FAILURE_DECRYPT_FAILED = 'decrypt_failed';
const AUTH_FAILURE_CORRUPT = 'corrupt';
const AUTH_FAILURE_INVALID = 'invalid';
const AUTH_FAILURE_ENCRYPTION_UNAVAILABLE = 'encryption_unavailable';
const AUTH_FAILURE_READ_FAILED = 'read_failed';

/**
 * Manages persistent storage of Supabase authentication sessions
 * to prevent creating multiple anonymous users across worktrees.
 *
 * Storage location: Platform-specific app data directory (INTERNAL DATA)
 * - macOS: ~/Library/Application Support/reliant/auth/reliant-auth.enc
 * - Windows: %APPDATA%\reliant\auth\reliant-auth.enc
 * - Linux: ~/.local/share/reliant/auth/reliant-auth.enc
 *
 * NOTE: Auth tokens are INTERNAL data (not user-editable), so they go in
 * the platform-specific app data dir, NOT in ~/.reliant/ (which is for
 * user-editable config like config.yaml, mcp.json, reliant.md).
 * 
 * Security: Uses Electron's safeStorage API for OS-level encryption:
 * - macOS: Keys stored in Keychain Access
 * - Windows: Keys generated via DPAPI (user-specific)
 * - Linux: Keys stored in kwallet/gnome-libsecret (if available)
 */
class AuthStorage {
  /**
   * @param {{ app?: object, safeStorage?: object }} [deps] Injectable Electron
   *   surfaces. Defaults to the real ones; tests pass fakes so the encryption
   *   and keychain behavior can be exercised without an Electron runtime.
   */
  constructor(deps = {}) {
    this.authFileName = 'reliant-auth.enc'; // .enc indicates encrypted
    this.legacyAuthFileName = 'reliant-auth.json'; // Old plaintext file
    this._app = deps.app || app;
    this._safeStorage = deps.safeStorage || safeStorage;

    // Latch holding the most recent unreadable-blob incident until something
    // reports it. The renderer learns about failures through `auth:load`, but
    // the FIRST read of a run is usually the main process's own (Statsig user
    // id, daemon PAT mint) — that read is what discovers and clears a dead
    // blob, and without this latch the renderer never hears about it.
    this.lastLoadFailure = null;
  }

  /**
   * Gets the full path to the encrypted auth storage file
   * @returns {string} Absolute path to reliant-auth.enc
   */
  getAuthPath() {
    const authDir = path.join(this._app.getPath('userData'), 'auth');
    return path.join(authDir, this.authFileName);
  }

  /**
   * Gets the path to the legacy plaintext auth file (for migration)
   * @returns {string} Absolute path to reliant-auth.json
   */
  getLegacyAuthPath() {
    const authDir = path.join(this._app.getPath('userData'), 'auth');
    return path.join(authDir, this.legacyAuthFileName);
  }

  /**
   * Ensures the auth directory exists
   */
  ensureAuthDirectory() {
    const authDir = path.join(this._app.getPath('userData'), 'auth');
    if (!fs.existsSync(authDir)) {
      fs.mkdirSync(authDir, { recursive: true });
    }
  }

  /**
   * Checks if safeStorage encryption is available
   * @returns {boolean} Whether encryption is available
   */
  isEncryptionAvailable() {
    try {
      return this._safeStorage.isEncryptionAvailable();
    } catch (error) {
      log.warn('[AuthStorage] safeStorage not available:', error.message);
      return false;
    }
  }

  /**
   * Migrates legacy plaintext auth to encrypted storage
   * @returns {boolean} Whether migration was performed
   */
  migrateLegacyAuth() {
    try {
      const legacyPath = this.getLegacyAuthPath();
      
      if (!fs.existsSync(legacyPath)) {
        return false;
      }

      // Read legacy plaintext data
      const plainData = fs.readFileSync(legacyPath, 'utf8');
      const session = JSON.parse(plainData);

      // Validate session structure
      if (!session || !session.access_token || !session.refresh_token) {
        log.warn('[AuthStorage] Legacy auth file invalid, removing');
        fs.unlinkSync(legacyPath);
        return false;
      }

      // Save using new encrypted format
      if (this.saveAuth(session)) {
        // Remove legacy plaintext file
        fs.unlinkSync(legacyPath);
        log.info('[AuthStorage] Successfully migrated auth to encrypted storage');
        return true;
      }

      return false;
    } catch (error) {
      log.error('[AuthStorage] Migration failed:', error.message);
      return false;
    }
  }

  /**
   * Reads the stored session and says WHICH of the three outcomes occurred.
   *
   * This is the real read path; `loadStoredAuth()` is a thin
   * session-or-null wrapper kept for the callers that genuinely only want the
   * session (Statsig user id, daemon PAT mint).
   *
   * The unrecoverable cases delete the blob HERE, exactly once, and record the
   * incident on `lastLoadFailure`. Deleting is not a nicety: leaving a blob
   * that can never be decrypted meant every subsequent read re-threw, re-logged
   * and returned null forever, so the app sat in a half-authenticated state
   * issuing RPCs that could not possibly carry a token. Clearing it converts an
   * indefinite silent failure into one honest "you need to sign in again".
   *
   * @returns {{ status: 'ok', session: Object }
   *          |{ status: 'empty', session: null }
   *          |{ status: 'unreadable', session: null, reason: string,
   *             cleared: boolean, recoverable: boolean, message?: string }}
   */
  readStoredAuth() {
    try {
      // First, try to migrate legacy auth if it exists
      this.migrateLegacyAuth();

      const authPath = this.getAuthPath();

      if (!fs.existsSync(authPath)) {
        return { status: AUTH_LOAD_EMPTY, session: null };
      }

      // Read encrypted data
      const encryptedData = fs.readFileSync(authPath);

      // Check if encryption is available.
      //
      // Deliberately NOT treated as unrecoverable: on Linux this is a locked
      // or not-yet-started keyring, which typically resolves on its own. The
      // ciphertext is still perfectly good, so destroying it here would sign
      // out a user whose session was never actually broken.
      if (!this.isEncryptionAvailable()) {
        log.warn('[AuthStorage] Encryption not available, cannot decrypt auth');
        return this._recordFailure({
          reason: AUTH_FAILURE_ENCRYPTION_UNAVAILABLE,
          cleared: false,
          recoverable: true,
        });
      }

      // Decrypt the data
      let decryptedString;
      try {
        decryptedString = this._safeStorage.decryptString(encryptedData);
      } catch (decryptError) {
        // The OS key that encrypted this blob is gone — the macOS keychain
        // item was deleted and recreated (each recreation mints a NEW random
        // key), the user migrated machines, or the OS keystore was reset.
        // Electron's error text names the ciphertext, which reads like file
        // corruption; it is really key loss, and no future run can undo it.
        log.error(
          '[AuthStorage] Stored session cannot be decrypted — the OS encryption key that ' +
          'wrote it is no longer available. Clearing it; the user must sign in again.',
          decryptError.message
        );
        return this._recordFailure({
          reason: AUTH_FAILURE_DECRYPT_FAILED,
          cleared: this._clearCorruptBlob(authPath),
          recoverable: false,
          message: decryptError.message,
        });
      }

      let session;
      try {
        session = JSON.parse(decryptedString);
      } catch (parseError) {
        // Decrypted cleanly but is not JSON: a truncated write. Also terminal.
        log.error('[AuthStorage] Decrypted session is not valid JSON:', parseError.message);
        return this._recordFailure({
          reason: AUTH_FAILURE_CORRUPT,
          cleared: this._clearCorruptBlob(authPath),
          recoverable: false,
          message: parseError.message,
        });
      }

      // Validate session structure
      if (!session || !session.access_token || !session.refresh_token) {
        log.warn('[AuthStorage] Decrypted session is missing required tokens');
        return this._recordFailure({
          reason: AUTH_FAILURE_INVALID,
          cleared: this._clearCorruptBlob(authPath),
          recoverable: false,
        });
      }

      // Expired is NOT a failure: Supabase refreshes from the refresh_token.
      if (session.expires_at) {
        const expiresAt = new Date(session.expires_at * 1000);
        if (expiresAt <= new Date()) {
          log.debug('[AuthStorage] Session expired, Supabase will handle refresh');
        }
      }

      return { status: AUTH_LOAD_OK, session };
    } catch (error) {
      // An unexpected I/O error (EACCES, EBUSY). Transient by assumption, so
      // nothing is deleted — see the recoverable/unrecoverable split above.
      log.error('[AuthStorage] Failed to load auth:', error.message);
      return this._recordFailure({
        reason: AUTH_FAILURE_READ_FAILED,
        cleared: false,
        recoverable: true,
        message: error.message,
      });
    }
  }

  /**
   * Deletes a blob that can never be decrypted again.
   * @returns {boolean} whether the file is gone afterwards.
   */
  _clearCorruptBlob(authPath) {
    try {
      fs.unlinkSync(authPath);
      return true;
    } catch (unlinkError) {
      // Worth surfacing: if this keeps failing the app will re-discover the
      // same dead blob on every launch.
      log.error(
        '[AuthStorage] Could not remove the unreadable auth file:',
        unlinkError.message
      );
      return false;
    }
  }

  /** Builds an `unreadable` result and latches it for the renderer. */
  _recordFailure({ reason, cleared, recoverable, message }) {
    const failure = {
      status: AUTH_LOAD_UNREADABLE,
      session: null,
      reason,
      cleared,
      recoverable,
      at: Date.now(),
    };
    if (message) failure.message = message;

    this.lastLoadFailure = failure;
    return failure;
  }

  /**
   * Most recent unreadable-blob incident, or null.
   *
   * `consume: true` clears the latch so a single incident is reported once
   * rather than re-firing on every subsequent `auth:load`.
   */
  takeLoadFailure({ consume = true } = {}) {
    const failure = this.lastLoadFailure;
    if (consume) this.lastLoadFailure = null;
    return failure;
  }

  /**
   * Loads stored authentication session from disk.
   *
   * Session-or-null convenience wrapper over `readStoredAuth()`. Callers that
   * must distinguish "not signed in" from "session unreadable" — chiefly the
   * `auth:load` IPC handler that feeds the renderer's Supabase adapter — should
   * call `readStoredAuth()` instead.
   *
   * @returns {Object|null} Session object, or null if absent or unreadable.
   */
  loadStoredAuth() {
    return this.readStoredAuth().session;
  }

  /**
   * Saves authentication session to disk (encrypted)
   * @param {Object} session - Supabase session object
   * @returns {boolean} Success status
   */
  saveAuth(session) {
    try {
      if (!session || !session.access_token) {
        log.warn('[AuthStorage] Cannot save: missing access_token');
        return false;
      }

      if (!session.refresh_token) {
        log.warn('[AuthStorage] Cannot save: missing refresh_token');
        return false;
      }

      // Check if encryption is available
      if (!this.isEncryptionAvailable()) {
        log.error('[AuthStorage] Encryption not available, refusing to save plaintext');
        return false;
      }

      this.ensureAuthDirectory();
      const authPath = this.getAuthPath();

      // Convert session to JSON string
      const plainText = JSON.stringify(session);

      // Encrypt using OS-level encryption
      const encryptedBuffer = this._safeStorage.encryptString(plainText);

      // Write encrypted data to file
      fs.writeFileSync(authPath, encryptedBuffer);

      // A successful write supersedes any earlier unreadable-blob incident:
      // the stored session is readable again by construction (it was just
      // encrypted with the CURRENT key). Leaving the latch set would report a
      // stale failure to the renderer after the user had already recovered.
      this.lastLoadFailure = null;

      log.debug('[AuthStorage] Auth saved successfully (encrypted)');
      return true;
    } catch (error) {
      log.error('[AuthStorage] Failed to save auth:', error.message);
      return false;
    }
  }

  /**
   * Clears stored authentication session
   * @returns {boolean} Success status
   */
  clearAuth() {
    try {
      const authPath = this.getAuthPath();
      const legacyPath = this.getLegacyAuthPath();

      // Remove encrypted file
      if (fs.existsSync(authPath)) {
        fs.unlinkSync(authPath);
        log.debug('[AuthStorage] Encrypted auth cleared');
      }

      // Also remove any legacy plaintext file
      if (fs.existsSync(legacyPath)) {
        fs.unlinkSync(legacyPath);
        log.debug('[AuthStorage] Legacy auth cleared');
      }

      // An explicit sign-out resolves any pending incident: there is nothing
      // left to warn the user about recovering.
      this.lastLoadFailure = null;

      return true;
    } catch (error) {
      log.error('[AuthStorage] Failed to clear auth:', error.message);
      return false;
    }
  }

  /**
   * Gets the storage backend being used (Linux only)
   * @returns {string} Backend name or 'n/a' for non-Linux
   */
  getStorageBackend() {
    if (process.platform !== 'linux') {
      return process.platform === 'darwin' ? 'keychain' : 'dpapi';
    }

    try {
      return this._safeStorage.getSelectedStorageBackend();
    } catch (error) {
      return 'unknown';
    }
  }
}

module.exports = new AuthStorage();
// Exported for tests and for the IPC layer that maps these onto the renderer's
// auth state. `AuthStorage` itself is exported so tests can construct an
// instance with fake `app` / `safeStorage` rather than booting Electron.
module.exports.AuthStorage = AuthStorage;
module.exports.AUTH_LOAD_OK = AUTH_LOAD_OK;
module.exports.AUTH_LOAD_EMPTY = AUTH_LOAD_EMPTY;
module.exports.AUTH_LOAD_UNREADABLE = AUTH_LOAD_UNREADABLE;
module.exports.AUTH_FAILURE_DECRYPT_FAILED = AUTH_FAILURE_DECRYPT_FAILED;
module.exports.AUTH_FAILURE_CORRUPT = AUTH_FAILURE_CORRUPT;
module.exports.AUTH_FAILURE_INVALID = AUTH_FAILURE_INVALID;
module.exports.AUTH_FAILURE_ENCRYPTION_UNAVAILABLE = AUTH_FAILURE_ENCRYPTION_UNAVAILABLE;
module.exports.AUTH_FAILURE_READ_FAILED = AUTH_FAILURE_READ_FAILED;
