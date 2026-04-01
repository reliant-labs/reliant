const fs = require('fs');
const path = require('path');
const { app } = require('electron');
const log = require('./logger');

/**
 * Migrates user data from old locations to the new path structure.
 * 
 * NEW STRUCTURE:
 * 
 * User Config (~/.reliant/) - user-editable, git-friendly:
 *   - config.yaml      # Models, shell settings
 *   - mcp.json         # Global MCP servers
 *   - reliant.md       # Global context/memories
 *   - worktrees/       # Git worktrees
 * 
 * Internal App Data (platform-specific) - managed by Reliant:
 *   - macOS: ~/Library/Application Support/reliant/
 *   - Windows: %APPDATA%\reliant\
 *   - Linux: ~/.local/share/reliant/
 *   Contains: data/, analytics/, auth/, cache/
 * 
 * Logs (platform-specific):
 *   - macOS: ~/Library/Logs/Reliant/
 *   - Windows: %APPDATA%\reliant\logs\
 *   - Linux: ~/.local/state/reliant/logs/
 */
class DataMigration {
  constructor() {
    this.homeDir = require('os').homedir();
    // User config dir: always ~/.reliant/
    this.userConfigDir = path.join(this.homeDir, '.reliant');
    // Internal app data: platform-specific
    this.appDataDir = app.getPath('userData');
    // Logs: platform-specific
    this.logsDir = app.getPath('logs');
  }

  /**
   * Run all migrations
   */
  async runMigrations() {
    log.info('[DataMigration] Starting data migrations...');
    log.info(`[DataMigration] User config dir: ${this.userConfigDir}`);
    log.info(`[DataMigration] App data dir: ${this.appDataDir}`);
    log.info(`[DataMigration] Logs dir: ${this.logsDir}`);
    
    const migrations = [
      this.ensureDirectories.bind(this),
      this.migrateDatabase.bind(this),
      this.migrateConfig.bind(this),
      this.migrateAuth.bind(this),
      this.migrateAnalytics.bind(this),
      this.migrateLogs.bind(this)
    ];

    for (const migration of migrations) {
      try {
        await migration();
      } catch (error) {
        log.error('[DataMigration] Migration failed:', error);
        // Continue with other migrations even if one fails
      }
    }

    log.info('[DataMigration] Data migrations completed');
  }

  /**
   * Ensure required directories exist
   */
  async ensureDirectories() {
    const dirs = [
      this.userConfigDir,
      path.join(this.userConfigDir, 'worktrees'),
      path.join(this.appDataDir, 'data'),
      path.join(this.appDataDir, 'auth'),
      path.join(this.appDataDir, 'analytics'),
      path.join(this.appDataDir, 'cache'),
    ];

    for (const dir of dirs) {
      if (!fs.existsSync(dir)) {
        fs.mkdirSync(dir, { recursive: true });
        log.debug(`[DataMigration] Created directory: ${dir}`);
      }
    }
  }

  /**
   * Migrate database to internal app data directory
   * Database is INTERNAL data, goes in platform-specific app data
   */
  async migrateDatabase() {
    const oldPaths = [
      path.join(this.homeDir, '.config', 'reliant', 'reliant.db'),
      path.join(this.userConfigDir, 'reliant.db'),  // Old location in ~/.reliant/
      path.join(this.appDataDir, 'reliant.db'),     // Old location directly in userData
    ];
    const newPath = path.join(this.appDataDir, 'data', 'reliant.db');

    for (const oldPath of oldPaths) {
      if (fs.existsSync(oldPath) && !fs.existsSync(newPath)) {
        log.info(`[DataMigration] Migrating database from ${oldPath} to ${newPath}`);
        await this.moveFile(oldPath, newPath);
        break;
      }
    }
  }

  /**
   * Migrate config files to ~/.reliant/
   * Config is USER-EDITABLE, goes in ~/.reliant/
   */
  async migrateConfig() {
    // Migrate config.yaml
    const configOldPaths = [
      path.join(this.appDataDir, '.reliant', 'config.yaml'),  // Old Electron location
      path.join(this.homeDir, '.config', 'reliant', 'config.yaml'),
    ];
    const configNewPath = path.join(this.userConfigDir, 'config.yaml');

    for (const oldPath of configOldPaths) {
      if (fs.existsSync(oldPath) && !fs.existsSync(configNewPath)) {
        log.info(`[DataMigration] Migrating config.yaml from ${oldPath} to ${configNewPath}`);
        await this.copyFile(oldPath, configNewPath);
        break;
      }
    }

    // Migrate mcp.json
    const mcpOldPaths = [
      path.join(this.appDataDir, '.reliant', 'mcp.json'),  // Old Electron location
      path.join(this.homeDir, '.config', 'reliant', 'mcp.json'),
    ];
    const mcpNewPath = path.join(this.userConfigDir, 'mcp.json');

    for (const oldPath of mcpOldPaths) {
      if (fs.existsSync(oldPath) && !fs.existsSync(mcpNewPath)) {
        log.info(`[DataMigration] Migrating mcp.json from ${oldPath} to ${mcpNewPath}`);
        await this.copyFile(oldPath, mcpNewPath);
        break;
      }
    }

    // Migrate reliant.md (global context)
    const contextOldPaths = [
      path.join(this.appDataDir, '.reliant', 'reliant.md'),
      path.join(this.homeDir, '.config', 'reliant', 'reliant.md'),
    ];
    const contextNewPath = path.join(this.userConfigDir, 'reliant.md');

    for (const oldPath of contextOldPaths) {
      if (fs.existsSync(oldPath) && !fs.existsSync(contextNewPath)) {
        log.info(`[DataMigration] Migrating reliant.md from ${oldPath} to ${contextNewPath}`);
        await this.copyFile(oldPath, contextNewPath);
        break;
      }
    }
  }

  /**
   * Migrate authentication tokens to internal app data
   * Auth is INTERNAL data, goes in platform-specific app data
   */
  async migrateAuth() {
    const oldPaths = [
      path.join(this.userConfigDir, 'auth.json'),  // Old location in ~/.reliant/
      path.join(this.appDataDir, 'auth.json'),     // Old location directly in userData
    ];
    const newPath = path.join(this.appDataDir, 'auth', 'auth.json');

    for (const oldPath of oldPaths) {
      if (fs.existsSync(oldPath) && !fs.existsSync(newPath)) {
        log.info(`[DataMigration] Migrating auth tokens from ${oldPath} to ${newPath}`);
        await this.moveFile(oldPath, newPath);
        break;
      }
    }
  }

  /**
   * Migrate analytics data to internal app data
   * Analytics is INTERNAL data, goes in platform-specific app data
   */
  async migrateAnalytics() {
    const oldPaths = [
      path.join(this.userConfigDir, 'analytics'),  // Old location in ~/.reliant/
      path.join(this.appDataDir, 'analytics'),     // Already in userData (just ensure dir)
    ];
    const newPath = path.join(this.appDataDir, 'analytics');

    for (const oldPath of oldPaths) {
      if (oldPath !== newPath && fs.existsSync(oldPath) && fs.statSync(oldPath).isDirectory()) {
        log.info(`[DataMigration] Migrating analytics from ${oldPath} to ${newPath}`);
        await this.moveDirectory(oldPath, newPath);
        break;
      }
    }
  }

  /**
   * Migrate log files to platform-specific logs directory
   */
  async migrateLogs() {
    const oldPath = path.join(this.userConfigDir, 'logs');

    if (fs.existsSync(oldPath) && fs.statSync(oldPath).isDirectory()) {
      log.info(`[DataMigration] Migrating logs from ${oldPath} to ${this.logsDir}`);
      
      // Copy log files, don't move them as user might want to keep originals
      const files = fs.readdirSync(oldPath);
      for (const file of files) {
        const oldFilePath = path.join(oldPath, file);
        const newFilePath = path.join(this.logsDir, file);
        
        if (!fs.existsSync(newFilePath) && fs.statSync(oldFilePath).isFile()) {
          await this.copyFile(oldFilePath, newFilePath);
        }
      }
    }
  }

  /**
   * Helper to move a file
   */
  async moveFile(src, dest) {
    // Ensure destination directory exists
    const destDir = path.dirname(dest);
    if (!fs.existsSync(destDir)) {
      fs.mkdirSync(destDir, { recursive: true });
    }

    // Copy then delete (safer than rename across filesystems)
    await this.copyFile(src, dest);
    fs.unlinkSync(src);
  }

  /**
   * Helper to copy a file
   */
  async copyFile(src, dest) {
    // Ensure destination directory exists
    const destDir = path.dirname(dest);
    if (!fs.existsSync(destDir)) {
      fs.mkdirSync(destDir, { recursive: true });
    }

    return new Promise((resolve, reject) => {
      const readStream = fs.createReadStream(src);
      const writeStream = fs.createWriteStream(dest);

      readStream.on('error', reject);
      writeStream.on('error', reject);
      writeStream.on('finish', resolve);

      readStream.pipe(writeStream);
    });
  }

  /**
   * Helper to move a directory
   */
  async moveDirectory(src, dest) {
    if (!fs.existsSync(dest)) {
      fs.mkdirSync(dest, { recursive: true });
    }

    const entries = fs.readdirSync(src, { withFileTypes: true });

    for (const entry of entries) {
      const srcPath = path.join(src, entry.name);
      const destPath = path.join(dest, entry.name);

      if (entry.isDirectory()) {
        await this.moveDirectory(srcPath, destPath);
      } else {
        await this.moveFile(srcPath, destPath);
      }
    }

    // Remove the old directory after successful migration
    fs.rmdirSync(src, { recursive: true });
  }
}

module.exports = DataMigration;
