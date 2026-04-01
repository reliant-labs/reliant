const log = require('electron-log');
const path = require('path');
const { app } = require('electron');
const fs = require('fs').promises;
const fsSync = require('fs');

// Configure log levels based on environment
const isDevelopment = process.env.NODE_ENV === 'development';
const isDebug = process.env.DEBUG === 'true' || process.env.DEBUG_ELECTRON === 'true';

// Check if we're in a packaged production build
// In production, never enable debug logging regardless of DEBUG env vars
const isPackaged = app && app.isPackaged;
const allowDebug = !isPackaged && (isDevelopment || isDebug);

// Set log levels
// File logs are more verbose for debugging in dev, info-only in production
log.transports.file.level = allowDebug ? 'debug' : 'info';
log.transports.console.level = allowDebug ? 'debug' : 'info';

// Configuration
const MAX_ARCHIVES = 10; // Keep 10 most recent archived logs
const MAX_AGE_DAYS = 30; // Delete archived logs older than 30 days (0 = disabled)
const MAX_SIZE = process.env.TEST_LOG_ROTATION_SIZE 
  ? parseFloat(process.env.TEST_LOG_ROTATION_SIZE) * 1024 * 1024 
  : 10 * 1024 * 1024; // 10MB default (use TEST_LOG_ROTATION_SIZE env var for testing, e.g., "0.1" for 100KB)

// Helper function to get log directory and path based on dev vs production
function getLogPaths() {
  if (!app || !app.getPath) {
    return { logDir: null, logPath: null };
  }

  let logDir, logPath;
  
  if (app.isPackaged) {
    // Production: use Electron's cross-platform logs directory
    // macOS: ~/Library/Logs/reliant/
    // Windows: %APPDATA%\reliant\logs\ (inside userData)
    // Linux: ~/.config/reliant/logs/ or $XDG_CONFIG_HOME/reliant/logs/ (inside userData)
    // Electron automatically handles platform-specific paths via app.getPath('logs')
    logDir = app.getPath('logs');
    logPath = path.join(logDir, 'main.log');
  } else {
    // Development: use workspace-specific path (matches backend behavior)
    // Each workspace/worktree gets its own logs
    // Note: process.cwd() is the electron/ directory, so we need to go up to project root
    // logger.js is in electron/src/, so __dirname is electron/src/
    // Going up two levels: ../.. gets us to project root
    const projectRoot = path.join(__dirname, '..', '..');
    logDir = path.join(projectRoot, '.reliant', 'logs');
    logPath = path.join(logDir, 'main.log');
  }

  return { logDir, logPath };
}

// Cleanup function to remove old archived logs
async function cleanupOldLogs(logDir, maxArchives = MAX_ARCHIVES, maxAgeDays = MAX_AGE_DAYS) {
  try {
    const files = await fs.readdir(logDir);
    const archivedFiles = files
      .filter(f => f.startsWith('main.') && f.endsWith('.log') && f !== 'main.log')
      .map(f => ({
        name: f,
        path: path.join(logDir, f),
        mtime: null, // Will be set below
        age: null // Will be set below
      }));

    if (archivedFiles.length === 0) {
      return; // No archives to clean up
    }

    const now = Date.now();
    const maxAgeMs = maxAgeDays > 0 ? maxAgeDays * 24 * 60 * 60 * 1000 : 0;

    // Get modification times and calculate age
    for (const file of archivedFiles) {
      try {
        const stat = await fs.stat(file.path);
        file.mtime = stat.mtime.getTime();
        file.age = now - file.mtime;
      } catch (err) {
        // Skip files that can't be stat'd
        log.warn(`[Logger] Failed to stat archive file ${file.name}:`, err.message);
        file.mtime = 0; // Put at end of list
        file.age = Infinity; // Mark as very old
      }
    }

    // Sort by modification time (newest first)
    archivedFiles.sort((a, b) => b.mtime - a.mtime);

    const toDelete = [];
    
    // Delete files older than maxAgeDays (if enabled)
    if (maxAgeDays > 0) {
      for (const file of archivedFiles) {
        if (file.age > maxAgeMs) {
          toDelete.push(file);
        }
      }
    }

    // Delete oldest files beyond maxArchives limit (but don't double-delete)
    const byCount = archivedFiles.slice(maxArchives);
    for (const file of byCount) {
      if (!toDelete.find(f => f.path === file.path)) {
        toDelete.push(file);
      }
    }

    // Delete the files
    for (const file of toDelete) {
      try {
        await fs.unlink(file.path);
        log.debug(`[Logger] Deleted old archive: ${file.name}`);
      } catch (err) {
        log.warn(`[Logger] Failed to delete old archive ${file.name}:`, err.message);
      }
    }

    if (toDelete.length > 0) {
      const ageDeleted = maxAgeDays > 0 ? archivedFiles.filter(f => f.age > maxAgeMs).length : 0;
      const countDeleted = toDelete.length - ageDeleted;
      const reasons = [];
      if (ageDeleted > 0) reasons.push(`${ageDeleted} older than ${maxAgeDays} day(s)`);
      if (countDeleted > 0) reasons.push(`${countDeleted} beyond limit of ${maxArchives}`);
      log.info(`[Logger] Cleaned up ${toDelete.length} old log archive(s) (${reasons.join(', ')})`);
    }
  } catch (error) {
    log.warn('[Logger] Failed to cleanup old logs:', error.message);
    // Don't throw - cleanup failure shouldn't break the app
  }
}

// Configure file transport
if (app && app.getPath) {
  const { logDir, logPath } = getLogPaths();

  if (logDir && logPath) {
    // Ensure the log directory exists
    if (!fsSync.existsSync(logDir)) {
      fsSync.mkdirSync(logDir, { recursive: true });
    }

    log.transports.file.resolvePathFn = () => {
      return logPath;
    };

    // Log the location for debugging
    console.log(`[Logger] Logs will be written to: ${logPath}`);
    console.log(`[Logger] Max file size: ${(MAX_SIZE / 1024).toFixed(2)} KB (${(MAX_SIZE / 1024 / 1024).toFixed(2)} MB)`);

    // Set max file size
    log.transports.file.maxSize = MAX_SIZE;

    // Helper function to manually trigger rotation if file exceeds size
    async function checkAndRotateIfNeeded() {
      try {
        const stat = await fs.stat(logPath);
        if (stat.size > MAX_SIZE) {
          console.log(`[Logger] ⚠️  Log file exceeds max size (${(stat.size / 1024).toFixed(2)}KB > ${(MAX_SIZE / 1024).toFixed(2)}KB), triggering rotation...`);
          
          // Get file info from electron-log
          const fileInfo = log.transports.file.getFile();
          if (fileInfo && fileInfo.path === logPath) {
            // Manually trigger rotation by calling archiveLogFn with file info
            // electron-log expects an object with path property
            await log.transports.file.archiveLogFn({ path: logPath, size: stat.size });
            console.log(`[Logger] ✅ Manual rotation completed`);
          } else {
            // Fallback: manually rename the file and electron-log will create a new one
            const timestamp = new Date().toISOString().replace(/[:.]/g, '-').split('.')[0];
            const info = path.parse(logPath);
            const archivePath = path.join(info.dir, `${info.name}.${timestamp}${info.ext}`);
            await fs.rename(logPath, archivePath);
            console.log(`[Logger] ✅ Manually rotated: ${path.basename(logPath)} -> ${path.basename(archivePath)}`);
            log.info(`[Logger] Log rotated: ${path.basename(logPath)} -> ${path.basename(archivePath)}`);
            
            // Cleanup old archives
            await cleanupOldLogs(info.dir, MAX_ARCHIVES, MAX_AGE_DAYS);
          }
        }
      } catch (err) {
        console.warn('[Logger] Failed to check/rotate on startup:', err.message);
      }
    }

    // Archive old logs - CRITICAL: Use archiveLogFn (not archiveLog) for electron-log v5.4.3
    // archiveLogFn can receive either a string path or an object with path property
    log.transports.file.archiveLogFn = async (oldLogPathOrInfo) => {
      // Handle both string path and object with path property
      const oldLogPath = typeof oldLogPathOrInfo === 'string' 
        ? oldLogPathOrInfo 
        : (oldLogPathOrInfo?.path || oldLogPathOrInfo);
      
      console.log(`[Logger] 🔄 archiveLogFn called! Old path: ${oldLogPath}`);
      try {
        const timestamp = new Date().toISOString().replace(/[:.]/g, '-').split('.')[0];
        const info = path.parse(oldLogPath.toString());
        const newFileName = `${info.name}.${timestamp}${info.ext}`;
        const archivePath = path.join(info.dir, newFileName);

        console.log(`[Logger] Archiving to: ${archivePath}`);

        // Rename the old log file
        await fs.rename(oldLogPath, archivePath);

        console.log(`[Logger] ✅ Log rotated: ${path.basename(oldLogPath)} -> ${path.basename(archivePath)}`);
        log.info(`[Logger] Log rotated: ${path.basename(oldLogPath)} -> ${path.basename(archivePath)}`);

        // Cleanup old archives after rotation
        await cleanupOldLogs(info.dir, MAX_ARCHIVES, MAX_AGE_DAYS);

        return archivePath;
      } catch (error) {
        console.error(`[Logger] ❌ Failed to archive log:`, error);
        log.error('[Logger] Failed to archive log:', error);
        // Don't throw - allow logging to continue even if archiving fails
        return oldLogPath;
      }
    };

    // Check if log file already exceeds size and rotate if needed
    // This handles the case where the file was already over the limit before rotation was enabled
    checkAndRotateIfNeeded().catch(err => {
      console.warn('[Logger] Failed to check/rotate on startup:', err.message);
    });

    // Run cleanup on startup to clean up any accumulated old logs
    // Don't await - let it run in background so it doesn't block app startup
    cleanupOldLogs(logDir, MAX_ARCHIVES, MAX_AGE_DAYS).catch(err => {
      log.warn('[Logger] Startup cleanup failed:', err.message);
    });
  }
}

// Format configuration
log.transports.file.format = '[{y}-{m}-{d} {h}:{i}:{s}.{ms}] [{level}] {text}';
log.transports.console.format = '[{h}:{i}:{s}.{ms}] [{level}] {text}';

// Catch errors in production (use isPackaged for reliable production detection)
if (isPackaged) {
  log.catchErrors({
    showDialog: false,
    onError: (error) => {
      log.error('Unhandled error:', error);
    }
  });
}

// Log initialization
if (app && app.getPath) {
  const { logPath } = getLogPaths();
  if (logPath) {
    // Check current log file size
    let currentSize = 0;
    try {
      const stat = fsSync.statSync(logPath);
      currentSize = stat.size;
    } catch (err) {
      // File doesn't exist yet
    }
    
    log.info(`[Logger] Electron logger initialized. Logs are being written to: ${logPath}`);
    log.info(`[Logger] Log level: file=${log.transports.file.level}, console=${log.transports.console.level}`);
    log.info(`[Logger] Max file size: ${(MAX_SIZE / 1024).toFixed(2)}KB (${(MAX_SIZE / 1024 / 1024).toFixed(2)}MB), Max archives: ${MAX_ARCHIVES}, Max age: ${MAX_AGE_DAYS > 0 ? MAX_AGE_DAYS + ' days' : 'disabled'}`);
    log.info(`[Logger] Current log file size: ${(currentSize / 1024).toFixed(2)}KB`);
    if (currentSize > MAX_SIZE) {
      log.warn(`[Logger] ⚠️  Log file already exceeds max size! Size: ${(currentSize / 1024).toFixed(2)}KB, Max: ${(MAX_SIZE / 1024).toFixed(2)}KB`);
      log.warn(`[Logger] Rotation should trigger on next log write`);
    }
  }
}

// Export a simplified interface
module.exports = {
  info: (...args) => log.info(...args),
  warn: (...args) => log.warn(...args),
  error: (...args) => log.error(...args),
  debug: (...args) => log.debug(...args),
  // Allow direct access to electron-log for advanced usage
  log: log,
  // Export the log path for reference
  getLogPath: () => {
    const { logPath } = getLogPaths();
    return logPath;
  }
};
