const path = require('path');
const fs = require('fs');
const { app, safeStorage } = require('electron');
const log = require('./logger');

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
  constructor() {
    this.authFileName = 'reliant-auth.enc'; // .enc indicates encrypted
    this.legacyAuthFileName = 'reliant-auth.json'; // Old plaintext file
  }

  /**
   * Gets the full path to the encrypted auth storage file
   * @returns {string} Absolute path to reliant-auth.enc
   */
  getAuthPath() {
    const authDir = path.join(app.getPath('userData'), 'auth');
    return path.join(authDir, this.authFileName);
  }

  /**
   * Gets the path to the legacy plaintext auth file (for migration)
   * @returns {string} Absolute path to reliant-auth.json
   */
  getLegacyAuthPath() {
    const authDir = path.join(app.getPath('userData'), 'auth');
    return path.join(authDir, this.legacyAuthFileName);
  }

  /**
   * Ensures the auth directory exists
   */
  ensureAuthDirectory() {
    const authDir = path.join(app.getPath('userData'), 'auth');
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
      return safeStorage.isEncryptionAvailable();
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
   * Loads stored authentication session from disk
   * @returns {Object|null} Session object or null if not found/invalid
   */
  loadStoredAuth() {
    try {
      // First, try to migrate legacy auth if it exists
      this.migrateLegacyAuth();

      const authPath = this.getAuthPath();

      if (!fs.existsSync(authPath)) {
        return null;
      }

      // Read encrypted data
      const encryptedData = fs.readFileSync(authPath);

      // Check if encryption is available
      if (!this.isEncryptionAvailable()) {
        log.warn('[AuthStorage] Encryption not available, cannot decrypt auth');
        return null;
      }

      // Decrypt the data
      let decryptedString;
      try {
        decryptedString = safeStorage.decryptString(encryptedData);
      } catch (decryptError) {
        log.error('[AuthStorage] Failed to decrypt auth data:', decryptError.message);
        // Remove corrupted file
        fs.unlinkSync(authPath);
        return null;
      }

      const session = JSON.parse(decryptedString);

      // Validate session structure
      if (!session || !session.access_token || !session.refresh_token) {
        log.warn('[AuthStorage] Decrypted session invalid');
        return null;
      }

      // Check if session is expired (but still return it - Supabase will refresh)
      if (session.expires_at) {
        const expiresAt = new Date(session.expires_at * 1000);
        const now = new Date();

        if (expiresAt <= now) {
          log.debug('[AuthStorage] Session expired, Supabase will handle refresh');
        }
      }

      return session;
    } catch (error) {
      log.error('[AuthStorage] Failed to load auth:', error.message);
      return null;
    }
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
      const encryptedBuffer = safeStorage.encryptString(plainText);

      // Write encrypted data to file
      fs.writeFileSync(authPath, encryptedBuffer);

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
      return safeStorage.getSelectedStorageBackend();
    } catch (error) {
      return 'unknown';
    }
  }
}

module.exports = new AuthStorage();
